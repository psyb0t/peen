package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	launchAgentCallID       = "call_launch"
	launchAgentGrandCallID  = "call_grand"
	launchAgentWorkspaceDir = "workspace"
	launchAgentChildName    = "child-agent"
	launchAgentGrandName    = "grandchild-agent"

	launchAgentChildAgentFile             = "---\nname: child-agent\ndescription: test child\n---\nDo the child task."
	launchAgentGrandAgentFile             = "---\nname: grandchild-agent\ndescription: test grandchild\n---\nDo the grandchild task."
	launchAgentRestrictedChildAgentFile   = "---\nname: child-agent\ndescription: test child\nallowed-tools: readFile\n---\nReview only."
	launchAgentDepthLimitedChildAgentFile = "---\nname: child-agent\ndescription: test child\nallowed-tools: readFile, launchAgent\n---\nReview and delegate when possible."
)

// launchAgentFixtureOptions parameterizes newLaunchAgentFixture beyond what
// the shared newRuntimeFixture (runtime_test.go) supports: extra named
// agent files, non-default agent run limits, and a last-word hook over the
// runtime options for settings a test drives rather than accepts.
type launchAgentFixtureOptions struct {
	AgentLimits AgentRunLimits
	AgentFiles  map[string]string
	Customize   func(*RuntimeOptions)
}

// newLaunchAgentFixture builds a runtime the same way newRuntimeFixture
// does, reusing its unexported helpers (writeRuntimeFile, NewStaticRegistry)
// from the same package, but with the extra agent files and agent run
// limits launchAgent's own tests need.
func newLaunchAgentFixture(
	t *testing.T,
	driver elelem.Driver,
	options launchAgentFixtureOptions,
) runtimeFixture {
	t.Helper()

	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	workspace := filepath.Join(root, launchAgentWorkspaceDir)
	require.NoError(t, os.MkdirAll(workspace, runtimeTestDirectoryMode))

	writeRuntimeFile(
		t,
		filepath.Join(configDirectory, ".agents", "agents", runtimeTestAgentName+".md"),
		runtimeTestAgentDocument,
	)

	for name, content := range options.AgentFiles {
		writeRuntimeFile(
			t,
			filepath.Join(configDirectory, ".agents", "agents", name+".md"),
			content,
		)
	}

	resolver, err := harness.NewResolver(configDirectory, harness.Limits{})
	require.NoError(t, err)

	stateDirectory := filepath.Join(root, "state")

	handle, err := db.Open(context.Background(), db.Config{
		Directory: stateDirectory,
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

	runtimeOptions := RuntimeOptions{
		Store:               store,
		Resolver:            resolver,
		Models:              registry,
		RootAgent:           runtimeTestAgentName,
		DefaultModel:        runtimeTestModelReference,
		DefaultWorkspace:    workspace,
		MaxContextTokens:    runtimeTestMaxContextTokens,
		ModelRequestTimeout: runtimeTestTurnTimeout,
		Events:              eventBus,
		AgentLimits:         options.AgentLimits,
		ConfigDirectory:     configDirectory,
		// Child compaction tests shrink the context below any real prompt.
		MaxSystemPromptPercent: runtimeTestPromptBudgetOff,
	}

	if options.Customize != nil {
		options.Customize(&runtimeOptions)
	}

	runtime, err := NewRuntime(context.Background(), runtimeOptions)
	require.NoError(t, err)

	// Children outlive the turn that launched them. Each one has to end, and
	// record that it ended, before the database closes under it.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			launchAgentShutdownBound,
		)
		defer cancel()

		require.NoError(t, runtime.ShutdownAgentRuns(ctx))
	})

	return runtimeFixture{
		runtime:         runtime,
		store:           store,
		handle:          handle,
		workspace:       workspace,
		eventBus:        eventBus,
		configDirectory: configDirectory,
		stateDirectory:  stateDirectory,
	}
}

func launchAgentArguments(t *testing.T, input launchAgentInput) string {
	t.Helper()

	encoded, err := json.Marshal(input)
	require.NoError(t, err)

	return string(encoded)
}

func decodeLaunchAgentOutput(t *testing.T, content string) launchAgentOutput {
	t.Helper()

	output := launchAgentOutput{}
	require.NoError(t, json.Unmarshal([]byte(toolOutputText(t, content)),
		&output))

	return output
}

// launchAgent returns as soon as the child runs, naming it. The child's final
// answer reaches the root later as an agent.finished session event whose data
// carries the answer, delivered at the root's next tool boundary.
func TestLaunchAgentReturnsAtOnceAndDeliversTheAnswerAsAnEvent(
	t *testing.T,
) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{Task: "say hi", Agent: launchAgentChildName},
		elelemtest.Text("child says hi"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	// The root's own stream is ordered. The child's events interleave with it
	// once the root waits, so they are checked apart.
	assert.Equal(
		t,
		[]string{
			EventTypeUserMessageCreated,
			EventTypeTurnStarted,
			EventTypeToolUse,
			EventTypeToolResult,
			EventTypeToolUse,
			EventTypeSessionEvents,
			EventTypeToolResult,
			EventTypeTurnCompleted,
		},
		eventsOfTypes(
			events,
			EventTypeUserMessageCreated,
			EventTypeTurnStarted,
			EventTypeToolUse,
			EventTypeToolResult,
			EventTypeSessionEvents,
			EventTypeTurnCompleted,
		),
	)
	assert.Equal(
		t,
		[]string{
			EventTypeAgentRunStarted,
			EventTypeAgentRunAssistantMessage,
			EventTypeAgentRunCompleted,
		},
		eventsOfTypes(
			events,
			EventTypeAgentRunStarted,
			EventTypeAgentRunAssistantMessage,
			EventTypeAgentRunCompleted,
		),
	)

	use := toolUsePayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolUse),
		&use,
	))
	assert.Equal(t, launchAgentCallID, use.CallID)
	assert.Equal(t, toolNameLaunchAgent, use.Name)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.Equal(t, launchAgentCallID, toolResult.CallID)
	assert.False(t, toolResult.IsError)

	output := decodeLaunchAgentOutput(t, toolResult.Content)
	assert.NotEqual(t, uuid.Nil, output.AgentRunID)
	assert.Equal(t, launchAgentChildName, output.Name)
	assert.Equal(t, AgentRunDefinitionStored, output.Definition)
	assert.Equal(t, rootAgentRunReference, output.ParentAgentRunID)

	notice := onlyDeliveredNotice(t, events)
	assert.Equal(t, agentFinishedEventType, notice.Type)

	data := agentEventData{}
	require.NoError(t, json.Unmarshal(notice.Data, &data))
	assert.Equal(t, output.AgentRunID, data.RunID)
	assert.Equal(t, "child says hi", data.Response)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)
	assert.Equal(
		t,
		[]string{taggedTask(t, "say hi")},
		tailTexts(childRequests[0].Messages, 1),
		"the child's task names the root as its sender",
	)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	toolMessage := messages.Items[2]
	assert.Equal(t, launchAgentCallID, toolMessage.ToolCallID)
	assert.False(t, toolMessage.IsError)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 1)
	assert.Equal(t, AgentRunStateCompleted, runs[0].Snapshot().State)
	assert.Equal(t, launchAgentCallID, runs[0].ParentToolCallID)

	query := repositories.Use(fixture.handle.GormDB)
	turn, err := query.Turn.WithContext(context.Background()).
		Where(query.Turn.ID.Eq(runs[0].ParentTurnID)).
		First()
	require.NoError(t, err)
	storedRun, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.ID.Eq(runs[0].ID)).
		First()
	require.NoError(t, err)
	assert.Equal(t, turn.WorkerGenerationID, storedRun.WorkerGenerationID)
}

// A child launch that succeeds is not an error: nothing in the launch path
// may log at error level, such as a wrap of a nil error. Swaps the process
// default logger, so it must not run in parallel.
func TestLaunchAgentSuccessfulLaunchLogsNoError(t *testing.T) {
	var captured bytes.Buffer

	previousLogger := slog.Default()

	slog.SetDefault(slog.New(slog.NewJSONHandler(&captured, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	driver := launchedChildDriver(
		t,
		launchAgentInput{Task: "say hi", Agent: launchAgentChildName},
		elelemtest.Text("child says hi"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	decoder := json.NewDecoder(&captured)

	for decoder.More() {
		record := map[string]any{}
		require.NoError(t, decoder.Decode(&record))
		assert.NotEqual(
			t,
			slog.LevelError.String(),
			record[slog.LevelKey],
			"unexpected error record: %v",
			record,
		)
	}
}
