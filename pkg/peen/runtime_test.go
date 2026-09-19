package peen_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/pkg/peen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testModelReference = "test/mock-model"
	testModelID        = "mock-model"
	testRootAgent      = "default"
	testMaxContextSize = 8192
	testFileMode       = 0o600
	testDirectoryMode  = 0o700
	testAgentDocument  = "---\nname: default\ndescription: test agent\n---\nFollow the test agent rules."
	testMalformedID    = "not-a-uuid"

	testWaitTimeout = 5 * time.Second
)

func TestNewInvalidOptions(t *testing.T) {
	t.Parallel()

	base := validOptions(t)

	testCases := []struct {
		name   string
		mutate func(*peen.Options)
	}{
		{
			name:   "missing config directory",
			mutate: func(o *peen.Options) { o.ConfigDirectory = "" },
		},
		{
			name:   "missing root agent",
			mutate: func(o *peen.Options) { o.RootAgent = "" },
		},
		{
			name:   "missing default model",
			mutate: func(o *peen.Options) { o.DefaultModel = "" },
		},
		{
			name:   "empty models",
			mutate: func(o *peen.Options) { o.Models = nil },
		},
		{
			name: "default model not present in models",
			mutate: func(o *peen.Options) {
				o.DefaultModel = "missing/model"
			},
		},
		{
			name: "compaction model not present in models",
			mutate: func(o *peen.Options) {
				o.CompactionModel = "missing/model"
			},
		},
		{
			name:   "negative max context tokens",
			mutate: func(o *peen.Options) { o.MaxContextTokens = -1 },
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			options := base
			tc.mutate(&options)

			_, err := peen.New(options)
			require.Error(t, err)
		})
	}
}

// TestRuntimeMessageRoundTrip and the other Runtime-constructing tests below
// do not run in parallel: db.Open writes the internal repositories package's
// process-global default handle, and running them concurrently races on it.
func TestRuntimeMessageRoundTrip(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("first reply"),
		elelemtest.Text("second reply"),
	)
	runtime := newTestRuntime(t, driver)

	first, err := runtime.Message(context.Background(), peen.MessageRequest{
		Message: "hello there",
	})
	require.NoError(t, err)
	assert.Equal(t, "first reply", first.Message)

	second, err := runtime.Message(context.Background(), peen.MessageRequest{
		Message: "and again",
	})
	require.NoError(t, err)
	assert.Equal(t, "second reply", second.Message)

	// Both turns ran against the same private session, which the caller never
	// names. The continuous transcript is the observable proof.
	page, err := runtime.ListMessages(
		context.Background(),
		peen.ListMessagesRequest{Order: peen.MessageOrderAsc},
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{"hello there", "first reply", "and again", "second reply"},
		messageContents(page.Items),
	)
}

func TestRuntimeStreamDeliversEventsIncrementally(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("streamed reply"))
	runtime := newTestRuntime(t, driver)

	events := make(chan peen.Event)
	done := make(chan streamOutcome, 1)

	go func() {
		result, err := runtime.Stream(
			context.Background(),
			peen.MessageRequest{Message: "stream this"},
			func(event peen.Event) error {
				events <- event

				return nil
			},
		)
		done <- streamOutcome{result: result, err: err}
	}()

	first := receiveEvent(t, events)
	assert.Equal(t, "user_message.created", first.Type)

	select {
	case <-done:
		t.Fatal("stream finished before the consumer read past the first event")
	default:
	}

	types := []string{first.Type}
	outcome := drainStream(t, events, done, &types)

	require.NoError(t, outcome.err)
	assert.Contains(t, types, "turn.started")
	assert.Contains(t, types, "turn.completed")
	assert.Greater(t, len(types), 1)
	assert.Equal(t, "streamed reply", outcome.result.Message)
}

func TestRuntimeListMessages(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("first reply"),
		elelemtest.Text("second reply"),
	)
	runtime := newTestRuntime(t, driver)

	_, err := runtime.Message(context.Background(), peen.MessageRequest{
		Message: "hello there",
	})
	require.NoError(t, err)

	_, err = runtime.Message(context.Background(), peen.MessageRequest{
		Message: "and again",
	})
	require.NoError(t, err)

	page, err := runtime.ListMessages(context.Background(), peen.ListMessagesRequest{
		Order: peen.MessageOrderAsc,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 4)
	assert.Equal(
		t,
		[]string{"hello there", "first reply", "and again", "second reply"},
		messageContents(page.Items),
	)
	assert.False(t, page.HasMore)

	firstPage, err := runtime.ListMessages(context.Background(), peen.ListMessagesRequest{
		Limit: 2,
		Order: peen.MessageOrderAsc,
	})
	require.NoError(t, err)
	assert.True(t, firstPage.HasMore)
	assert.Equal(t, 2, firstPage.Limit)
	assert.Equal(
		t,
		[]string{"hello there", "first reply"},
		messageContents(firstPage.Items),
	)
}

func TestRuntimeDetails(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("hi"))
	runtime := newTestRuntime(t, driver)

	_, err := runtime.Message(context.Background(), peen.MessageRequest{
		Message: "hello",
	})
	require.NoError(t, err)

	details, err := runtime.Details(context.Background())
	require.NoError(t, err)
	assert.Equal(t, testRootAgent, details.Agent)
	assert.Equal(t, int64(1), details.CompletedTurnCount)
	assert.False(t, details.ActiveTurn)
}

func TestRuntimeCancelHeldTurn(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("first reply"),
		elelemtest.Text("second reply"),
	)
	runtime := newTestRuntime(t, driver)

	_, err := runtime.Message(context.Background(), peen.MessageRequest{
		Message: "hello there",
	})
	require.NoError(t, err)

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan streamOutcome, 1)

	go func() {
		result, err := runtime.Stream(
			context.Background(),
			peen.MessageRequest{
				Message: "please wait",
			},
			func(event peen.Event) error {
				if event.Type == "turn.started" {
					close(started)
					<-release
				}

				return nil
			},
		)
		done <- streamOutcome{result: result, err: err}
	}()

	select {
	case <-started:
	case <-time.After(testWaitTimeout):
		t.Fatal("held turn did not start in time")
	}

	cancelResult, err := runtime.Cancel(context.Background())
	require.NoError(t, err)
	assert.True(t, cancelResult.CancelRequested)

	close(release)

	select {
	case outcome := <-done:
		require.Error(t, outcome.err)
		assert.ErrorIs(t, outcome.err, commerr.ErrCancelled)
	case <-time.After(testWaitTimeout):
		t.Fatal("held turn did not finish after cancellation")
	}
}

// The direct runtime takes no session ID anywhere in its contract. Reads,
// details, and cancellation all address its own workspace, so there is no
// identifier for a caller to get wrong.
func TestRuntimeReadsNeedNoSessionIdentifier(t *testing.T) {
	runtime := newTestRuntime(t, elelemtest.NewScriptedDriver())

	page, err := runtime.ListMessages(
		context.Background(),
		peen.ListMessagesRequest{},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Items)

	details, err := runtime.Details(context.Background())
	require.NoError(t, err)
	assert.Equal(t, testRootAgent, details.Agent)

	cancelled, err := runtime.Cancel(context.Background())
	require.NoError(t, err)
	assert.False(t, cancelled.CancelRequested)
}

// The direct facade addresses one workspace, so nothing it hands back may name
// a session. A session ID here would invite a caller to route on it, which is
// a control-plane job this runtime does not do.
func TestPublicTypesCarryNoSessionIdentifier(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		value any
	}{
		{name: "SessionDetails", value: peen.SessionDetails{}},
		{name: "MessageResult", value: peen.MessageResult{}},
		{name: "ListMessagesRequest", value: peen.ListMessagesRequest{}},
		{name: "ListMessagesResult", value: peen.ListMessagesResult{}},
		{name: "MessageRequest", value: peen.MessageRequest{}},
		{name: "CancelResult", value: peen.CancelResult{}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fields := reflect.TypeOf(tc.value)
			for field := range fields.Fields() {
				field := field.Name
				assert.NotContains(
					t,
					strings.ToLower(field),
					"session",
					"%s.%s names a session", tc.name, field,
				)
			}
		})
	}

	assert.NotContains(
		t,
		exportedFieldNames(peen.SessionDetails{}),
		"ID",
		"SessionDetails still exposes a session identifier",
	)
}

func exportedFieldNames(value any) []string {
	valueType := reflect.TypeOf(value)
	names := make([]string, 0, valueType.NumField())

	for field := range valueType.Fields() {
		names = append(names, field.Name)
	}

	return names
}

type streamOutcome struct {
	result peen.MessageResult
	err    error
}

// receiveEvent waits for one event with a bounded timeout so a broken sink
// fails the test instead of hanging the suite.
func receiveEvent(t *testing.T, events <-chan peen.Event) peen.Event {
	t.Helper()

	select {
	case event := <-events:
		return event
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for an event")

		return peen.Event{}
	}
}

// drainStream reads every remaining event off the channel, appending each
// type to types, until the streaming goroutine reports its outcome.
func drainStream(
	t *testing.T,
	events <-chan peen.Event,
	done <-chan streamOutcome,
	types *[]string,
) streamOutcome {
	t.Helper()

	for {
		select {
		case event := <-events:
			*types = append(*types, event.Type)
		case outcome := <-done:
			return outcome
		case <-time.After(testWaitTimeout):
			t.Fatal("timed out draining the event stream")

			return streamOutcome{}
		}
	}
}

func messageContents(messages []peen.Message) []string {
	values := make([]string, 0, len(messages))
	for _, message := range messages {
		values = append(values, message.Content)
	}

	return values
}

// validOptions returns a fully valid Options value for the invalid-options
// table test to mutate one field at a time.
func validOptions(t *testing.T) peen.Options {
	t.Helper()

	configDirectory, _ := writeTestHarness(t)

	return peen.Options{
		ConfigDirectory: configDirectory,
		RootAgent:       testRootAgent,
		Models: map[string]peen.ModelClient{
			testModelReference: {
				Client: elelem.New(elelemtest.NewScriptedDriver()),
				Model:  elelem.Model{ID: testModelID},
			},
		},
		DefaultModel:     testModelReference,
		MaxContextTokens: testMaxContextSize,
		TurnTimeout:      time.Minute,
	}
}

// newTestRuntime builds a Runtime over a real, temporary SQLite store, the
// way an embedding caller would, using only exported pkg/peen surface.
func newTestRuntime(t *testing.T, driver elelem.Driver) *peen.Runtime {
	t.Helper()

	configDirectory, workspace := writeTestHarness(t)
	t.Chdir(workspace)

	runtime, err := peen.New(peen.Options{
		ConfigDirectory: configDirectory,
		RootAgent:       testRootAgent,
		Models: map[string]peen.ModelClient{
			testModelReference: {
				Client: elelem.New(driver),
				Model:  elelem.Model{ID: testModelID},
			},
		},
		DefaultModel:     testModelReference,
		MaxContextTokens: testMaxContextSize,
		TurnTimeout:      time.Minute,
	})
	require.NoError(t, err)

	return runtime
}

// writeTestHarness lays out the AGENTS.md and named agent definition the
// harness resolver and the configured root agent both require, plus a
// separate workspace used as the runtime's startup directory.
func writeTestHarness(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	workspace := filepath.Join(root, "workspace")

	writeTestFile(t, filepath.Join(configDirectory, "AGENTS.md"), "Follow config rules.")
	writeTestFile(
		t,
		filepath.Join(configDirectory, ".agents", "agents", testRootAgent+".md"),
		testAgentDocument,
	)
	require.NoError(t, os.MkdirAll(workspace, testDirectoryMode))

	return configDirectory, workspace
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), testDirectoryMode))
	require.NoError(t, os.WriteFile(path, []byte(content), testFileMode))
}
