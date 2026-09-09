package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	runtimeTestModelReference = "test/mock-model"
	runtimeTestModelID        = "mock-model"
	runtimeTestAgentName      = "default"
	runtimeTestTurnTimeout    = time.Minute
	runtimeTestFileMode       = 0o600
	runtimeTestDirectoryMode  = 0o700

	// runtimeTestMaxContextTokens is far above anything the fixture's turns
	// count to, so a test that does not set out to hit the limit never does.
	runtimeTestMaxContextTokens = 8192

	runtimeTestAgentDocument = "---\nname: default\ndescription: test agent\n---\nFollow the test agent rules."
	runtimeTestConfigRules   = "Follow config rules."
	runtimeTestWorkspaceRule = "Follow workspace rules."
	runtimeTestOtherRule     = "Follow the other workspace rules."
)

func TestRuntimeRunPersistsTranscriptEventsAndSnapshots(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Thinking("inspect state", "done"),
	))
	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "inspect the workspace",
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			events = append(events, event)

			return nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Created)
	assert.Equal(t, "done", result.Text)
	assert.Equal(t, "inspect state", result.Thinking)
	assert.Equal(
		t,
		[]string{
			eventTypeTurnStarted,
			eventTypeTurnCompleted,
		},
		harnessEventTypes(events),
	)

	// The model's own output now reaches the transcript as content blocks
	// under the names that went on the wire, not under Peen-specific delta
	// names, so a stored turn and a streamed turn describe the same thing.
	assert.Equal(
		t,
		[]string{
			essessey.EventTypeContentBlockStart,
			essessey.EventTypeContentBlockDelta,
			essessey.EventTypeContentBlockStop,
			essessey.EventTypeContentBlockStart,
			essessey.EventTypeContentBlockDelta,
			essessey.EventTypeContentBlockStop,
		},
		protocolEventTypes(events),
	)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	require.Len(t, messages.Items, 2)
	assert.Equal(t, []string{"inspect the workspace", "done"}, messageContents(messages.Items))
	assert.Equal(
		t,
		[]models.MessageRole{models.MessageRoleUser, models.MessageRoleAssistant},
		messageRoles(messages.Items),
	)
	assert.Equal(t, fixture.workspace, messages.Items[0].Workspace)
	assert.Equal(t, "test/mock-model", messages.Items[1].ModelID)

	query := repositories.Use(fixture.handle.GormDB)
	turn, err := query.Turn.WithContext(context.Background()).
		Where(query.Turn.SessionID.Eq(result.SessionID)).
		First()
	require.NoError(t, err)
	assert.Equal(t, string(models.TurnStateCompleted), string(turn.State))
	require.NotNil(t, turn.ContextSnapshotHash)
	require.NotNil(t, turn.PromptSnapshotHash)

	contextSnapshot, err := query.ContextSnapshot.WithContext(context.Background()).
		Where(query.ContextSnapshot.Hash.Eq(*turn.ContextSnapshotHash)).
		First()
	require.NoError(t, err)
	assert.Contains(t, contextSnapshot.ResolvedContent, runtimeTestConfigRules)
	assert.Contains(t, contextSnapshot.ResolvedContent, runtimeTestWorkspaceRule)

	promptSnapshot, err := query.PromptSnapshot.WithContext(context.Background()).
		Where(query.PromptSnapshot.Hash.Eq(*turn.PromptSnapshotHash)).
		First()
	require.NoError(t, err)
	assert.Equal(t, contextSnapshot.ResolvedContent, promptSnapshot.EffectivePrompt)

	persistedEvents, err := query.Event.WithContext(context.Background()).
		Where(query.Event.SessionID.Eq(result.SessionID)).
		Order(query.Event.Sequence.Asc()).
		Find()
	require.NoError(t, err)
	assert.Equal(t, eventTypes(events), persistedEventTypes(persistedEvents))
}

func TestRuntimeResumesSessionWithCurrentWorkspace(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("first reply"), elelemtest.Text("second reply"))
	fixture := newRuntimeFixture(t, driver)
	first, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "first request",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	second, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &first.SessionID,
		Message:   "second request",
		Workspace: fixture.otherWorkspace,
		RequestID: uuid.New(),
		Model:     runtimeTestModelReference,
	})
	require.NoError(t, err)
	assert.False(t, second.Created)
	assert.Equal(t, first.SessionID, second.SessionID)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Contains(t, requests[1].Messages[0].Text(), runtimeTestOtherRule)
	assert.NotContains(t, requests[1].Messages[0].Text(), runtimeTestWorkspaceRule)
	assert.Equal(
		t,
		[]string{"first request", "first reply", "second request"},
		messageTexts(requests[1].Messages[1:]),
	)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		first.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{
			fixture.workspace,
			fixture.workspace,
			fixture.otherWorkspace,
			fixture.otherWorkspace,
		},
		messageWorkspaces(messages.Items),
	)
}

func TestRuntimePerTurnSystemPromptModes(t *testing.T) {
	testCases := []struct {
		name              string
		mode              PromptMode
		wantBasePrompt    bool
		wantRequestPrompt bool
	}{
		{
			name:              "append retains base prompt",
			mode:              PromptModeAppend,
			wantBasePrompt:    true,
			wantRequestPrompt: true,
		},
		{
			name:              "replace drops base prompt",
			mode:              PromptModeReplace,
			wantBasePrompt:    false,
			wantRequestPrompt: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
			fixture := newRuntimeFixture(t, driver)
			_, err := fixture.runtime.Run(context.Background(), TurnRequest{
				Message:          "test the prompt",
				Workspace:        fixture.workspace,
				SystemPrompt:     "request prompt",
				SystemPromptMode: tc.mode,
			})
			require.NoError(t, err)

			request, ok := driver.LastRequest()
			require.True(t, ok)
			systemPrompt := request.Messages[0].Text()
			assert.Equal(t, tc.wantBasePrompt, containsBasePrompt(systemPrompt))
			assert.Equal(t, tc.wantRequestPrompt, containsRequestPrompt(systemPrompt))
		})
	}
}

func newRuntimeFixture(
	t *testing.T,
	driver elelem.Driver,
) runtimeFixture {
	t.Helper()

	return newRuntimeFixtureWithOptions(t, driver, nil)
}

// newRuntimeFixtureWithOptions is newRuntimeFixture with a hook that changes
// the runtime options before construction, for the settings a test needs to
// drive rather than accept.
func newRuntimeFixtureWithOptions(
	t *testing.T,
	driver elelem.Driver,
	customize func(*RuntimeOptions),
) runtimeFixture {
	t.Helper()

	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	workspace := filepath.Join(root, "workspace")
	otherWorkspace := filepath.Join(root, "other-workspace")
	writeRuntimeFile(t, filepath.Join(configDirectory, "AGENTS.md"), runtimeTestConfigRules)
	writeRuntimeFile(t, filepath.Join(workspace, "AGENTS.md"), runtimeTestWorkspaceRule)
	writeRuntimeFile(t, filepath.Join(otherWorkspace, "AGENTS.md"), runtimeTestOtherRule)
	writeRuntimeFile(
		t,
		filepath.Join(configDirectory, ".agents", "agents", runtimeTestAgentName+".md"),
		runtimeTestAgentDocument,
	)

	resolver, err := harness.NewResolver(configDirectory, harness.Limits{})
	require.NoError(t, err)
	handle, err := db.Open(context.Background(), db.Config{
		Directory: filepath.Join(root, "state"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	registry, err := NewStaticRegistry(map[string]ModelClient{
		runtimeTestModelReference: {
			Client: elelem.New(driver),
			Model:  elelem.Model{ID: runtimeTestModelID},
		},
	})
	require.NoError(t, err)
	eventBus := events.NewBus(events.Options{})
	options := RuntimeOptions{
		Store:            store,
		Resolver:         resolver,
		Models:           registry,
		RootAgent:        runtimeTestAgentName,
		DefaultModel:     runtimeTestModelReference,
		DefaultWorkspace: workspace,
		MaxContextTokens: runtimeTestMaxContextTokens,
		TurnTimeout:      runtimeTestTurnTimeout,
		Events:           eventBus,
		ConfigDirectory:  configDirectory,
	}

	if customize != nil {
		customize(&options)
	}

	runtime, err := NewRuntime(options)
	require.NoError(t, err)

	return runtimeFixture{
		runtime:         runtime,
		store:           store,
		handle:          handle,
		workspace:       workspace,
		otherWorkspace:  otherWorkspace,
		eventBus:        eventBus,
		configDirectory: configDirectory,
	}
}

type runtimeFixture struct {
	runtime         *Runtime
	store           *session.Store
	handle          *db.Handle
	workspace       string
	otherWorkspace  string
	eventBus        *events.Bus
	configDirectory string
}

func writeRuntimeFile(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), runtimeTestDirectoryMode))
	//nolint:gosec // Test fixture paths are created under t.TempDir.
	require.NoError(t, os.WriteFile(path, []byte(content), runtimeTestFileMode))
}

func eventTypes(events []Event) []string {
	values := make([]string, 0, len(events))
	for _, event := range events {
		values = append(values, event.Type)
	}

	return values
}

// harnessEventTypes keeps only Peen's own lifecycle records.
//
// The transcript holds two record classes in one sequence: harness records
// under Peen's names, and protocol records under the exact essessey names that
// went on the wire. A test about turn lifecycle should not have to restate
// every content block the model happened to produce, so this drops the
// protocol half and leaves the half it is asserting about.
func harnessEventTypes(events []Event) []string {
	values := make([]string, 0, len(events))

	for _, event := range events {
		if isProtocolEventType(event.Type) {
			continue
		}

		values = append(values, event.Type)
	}

	return values
}

// protocolEventTypes keeps only the records that went on the wire.
func protocolEventTypes(events []Event) []string {
	values := make([]string, 0, len(events))

	for _, event := range events {
		if !isProtocolEventType(event.Type) {
			continue
		}

		values = append(values, event.Type)
	}

	return values
}

func isProtocolEventType(eventType string) bool {
	switch eventType {
	case essessey.EventTypeMessageStart,
		essessey.EventTypePing,
		essessey.EventTypeContentBlockStart,
		essessey.EventTypeContentBlockDelta,
		essessey.EventTypeContentBlockStop,
		essessey.EventTypeMessageDelta,
		essessey.EventTypeMessageStop:
		return true
	default:
		return false
	}
}

func persistedEventTypes(events []*models.Event) []string {
	values := make([]string, 0, len(events))
	for _, event := range events {
		values = append(values, event.EventType)
	}

	return values
}

func messageContents(messages []*models.Message) []string {
	values := make([]string, 0, len(messages))
	for _, message := range messages {
		values = append(values, message.Content)
	}

	return values
}

func messageRoles(messages []*models.Message) []models.MessageRole {
	values := make([]models.MessageRole, 0, len(messages))
	for _, message := range messages {
		values = append(values, message.Role)
	}

	return values
}

func messageWorkspaces(messages []*models.Message) []string {
	values := make([]string, 0, len(messages))
	for _, message := range messages {
		values = append(values, message.Workspace)
	}

	return values
}

func messageTexts(messages []elelem.Message) []string {
	values := make([]string, 0, len(messages))
	for _, message := range messages {
		values = append(values, message.Text())
	}

	return values
}

func containsBasePrompt(value string) bool {
	return strings.Contains(value, defaultSystemPrompt)
}

func containsRequestPrompt(value string) bool {
	return strings.Contains(value, "request prompt")
}
