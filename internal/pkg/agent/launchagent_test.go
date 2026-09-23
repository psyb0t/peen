package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
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
	launchAgentCallID       = "call_launch"
	launchAgentGrandCallID  = "call_grand"
	launchAgentWorkspaceDir = "workspace"
	launchAgentChildName    = "child-agent"
	launchAgentGrandName    = "grandchild-agent"

	launchAgentChildAgentFile             = "---\nname: child-agent\ndescription: test child\n---\nDo the child task."
	launchAgentGrandAgentFile             = "---\nname: grandchild-agent\ndescription: test grandchild\n---\nDo the grandchild task."
	launchAgentRestrictedChildAgentFile   = "---\nname: child-agent\ndescription: test child\nallowed-tools: read_file\n---\nReview only."
	launchAgentDepthLimitedChildAgentFile = "---\nname: child-agent\ndescription: test child\nallowed-tools: read_file, launch_agent\n---\nReview and delegate when possible."
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
// limits launch_agent's own tests need.
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
		Store:            store,
		Resolver:         resolver,
		Models:           registry,
		RootAgent:        runtimeTestAgentName,
		DefaultModel:     runtimeTestModelReference,
		DefaultWorkspace: workspace,
		MaxContextTokens: 8192,
		TurnTimeout:      runtimeTestTurnTimeout,
		Events:           eventBus,
		AgentLimits:      options.AgentLimits,
		ConfigDirectory:  configDirectory,
	}

	if options.Customize != nil {
		options.Customize(&runtimeOptions)
	}

	runtime, err := NewRuntime(context.Background(), runtimeOptions)
	require.NoError(t, err)

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
	require.NoError(t, json.Unmarshal([]byte(content), &output))

	return output
}

// One scripted launch_agent call must produce a paired tool_use and
// tool_result on the parent's event stream, a durable tool-role message, and
// return the named child's final answer.
func TestLaunchAgentNamedAgentReturnsFinalResponse(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "say hi",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.Text("child says hi"),
		elelemtest.Text("done"),
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

	// The completed run's own agent.finished notice lands on this same
	// session's event bus, and this tool call's own post-run boundary is the
	// very next place a pending event is delivered, so it surfaces here as
	// an ordinary session.events message: a child agent gets no private
	// notification path, only the normal session event path every producer
	// uses.
	assert.Equal(
		t,
		[]string{
			EventTypeUserMessageCreated,
			EventTypeTurnStarted,
			EventTypeToolUse,
			EventTypeAgentRunStarted,
			EventTypeAgentRunTextDelta,
			EventTypeAgentRunAssistantMessage,
			EventTypeAgentRunCompleted,
			EventTypeSessionEvents,
			EventTypeToolResult,
			EventTypeTurnCompleted,
		},
		harnessEventTypes(events),
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
	assert.Equal(t, launchAgentChildName, output.Name)
	assert.Equal(t, AgentRunDefinitionStored, output.Definition)
	assert.Equal(t, "child says hi", output.Response)
	assert.False(t, output.Cancelled)

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

func TestLaunchAgentCarriesExplicitSkillIntoChildPrompt(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "review the workspace",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.Text("child finished"),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})
	writeRuntimeFile(
		t,
		filepath.Join(
			fixture.workspace,
			".agents",
			"skills",
			runtimeTestExplicitSkillName,
			"SKILL.md",
		),
		"---\nname: review-rules\ndescription: Review rules.\n---\n"+
			runtimeTestWorkspaceSkillInstructions,
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   runtimeTestExplicitSkillMessage,
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	requests := driver.Requests()
	require.Len(t, requests, 3)
	assert.Contains(
		t,
		requests[1].Messages[0].Text(),
		runtimeTestWorkspaceSkillInstructions,
	)

	query := repositories.Use(fixture.handle.GormDB)
	storedRun, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.SessionID.Eq(result.SessionID)).
		First()
	require.NoError(t, err)
	assert.Contains(
		t,
		storedRun.SystemPrompt,
		runtimeTestWorkspaceSkillInstructions,
	)
}

func TestLaunchAgentAllowedToolsExcludeWrites(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "review the workspace",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.ToolCall(
			"call_restricted_write",
			toolNameWriteFile,
			`{"path":"must-not-exist.txt","content":"blocked"}`,
		),
		elelemtest.Text("review complete"),
		elelemtest.Text("parent complete"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentRestrictedChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the reviewer",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "parent complete", result.Text)

	_, err = os.Stat(filepath.Join(fixture.workspace, "must-not-exist.txt"))
	assert.True(t, os.IsNotExist(err))
}

func TestLaunchAgentAdHocDefinitionReturnsFinalResponse(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task: "say hi",
				AgentDefinition: &agentDefinitionInput{
					Name:         "scratch",
					Instructions: "do the ad-hoc task",
				},
			}),
		),
		elelemtest.Text("child says hi"),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the ad-hoc child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	toolMessage := messages.Items[2]

	output := decodeLaunchAgentOutput(t, toolMessage.Content)
	assert.Equal(t, "scratch", output.Name)
	assert.Equal(t, AgentRunDefinitionAdHoc, output.Definition)
	assert.Equal(t, "child says hi", output.Response)
}

func TestLaunchAgentBothFormsIsValidationFailure(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "say hi",
				Agent: launchAgentChildName,
				AgentDefinition: &agentDefinitionInput{
					Name:         "scratch",
					Instructions: "do the task",
				},
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch with both forms",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err, "a validation failure is a tool error, not a turn failure")
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "exactly one")
}

func TestLaunchAgentNeitherFormIsValidationFailure(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{Task: "say hi"}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch with neither form",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "exactly one")
}

func TestLaunchAgentUnknownAgentListsEffectiveNames(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "say hi",
				Agent: "does-not-exist",
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch an unknown agent",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "not found")
	assert.Contains(t, toolResult.Content, launchAgentChildName)
}

func TestLaunchAgentOversizedAdHocInstructionsRejected(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task: "say hi",
				AgentDefinition: &agentDefinitionInput{
					Name:         "scratch",
					Instructions: strings.Repeat("x", 100),
				},
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentLimits: AgentRunLimits{MaxAdHocInstructionBytes: 8},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch with an oversized ad-hoc definition",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "too large")
}

// A parent launches a child, and that child itself launches a grandchild,
// proving launch_agent's recursion end to end.
func TestLaunchAgentRecursion(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "delegate",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.ToolCall(
			launchAgentGrandCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "do the grandchild work",
				Agent: launchAgentGrandName,
			}),
		),
		elelemtest.Text("grandchild says hi"),
		elelemtest.Text("child final, saw: grandchild says hi"),
		elelemtest.Text("parent final"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
			launchAgentGrandName: launchAgentGrandAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "start the chain",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "parent final", result.Text)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	toolMessage := messages.Items[2]
	output := decodeLaunchAgentOutput(t, toolMessage.Content)
	assert.Equal(t, "child final, saw: grandchild says hi", output.Response)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 2)

	byDepth := map[int]*AgentRun{}
	for _, run := range runs {
		byDepth[run.Depth] = run
	}

	require.Contains(t, byDepth, 1)
	require.Contains(t, byDepth, 2)
	assert.Equal(t, launchAgentChildName, byDepth[1].Name)
	assert.Equal(t, launchAgentGrandName, byDepth[2].Name)
	assert.Equal(t, AgentRunStateCompleted, byDepth[1].Snapshot().State)
	assert.Equal(t, AgentRunStateCompleted, byDepth[2].Snapshot().State)
	assert.Nil(t, byDepth[1].ParentAgentRunID)
	require.NotNil(t, byDepth[2].ParentAgentRunID)
	assert.Equal(t, byDepth[1].ID, *byDepth[2].ParentAgentRunID)
	assert.Equal(t, launchAgentCallID, byDepth[1].ParentToolCallID)
	assert.Equal(t, launchAgentGrandCallID, byDepth[2].ParentToolCallID)

	query := repositories.Use(fixture.handle.GormDB)
	storedChild, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.ID.Eq(byDepth[1].ID)).
		First()
	require.NoError(t, err)
	assert.Nil(t, storedChild.ParentAgentRunID)
	assert.Equal(t, launchAgentCallID, storedChild.ParentToolCallID)

	storedGrandchild, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.ID.Eq(byDepth[2].ID)).
		First()
	require.NoError(t, err)
	require.NotNil(t, storedGrandchild.ParentAgentRunID)
	assert.Equal(t, byDepth[1].ID, *storedGrandchild.ParentAgentRunID)
	assert.Equal(t, launchAgentGrandCallID, storedGrandchild.ParentToolCallID)
}

// A child at the configured limit cannot request more child work because the
// model never receives the launch_agent tool definition.
func TestLaunchAgentAtDepthLimitDoesNotExposeLaunchAgent(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "delegate",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.Text("child final"),
		elelemtest.Text("parent final"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentLimits: AgentRunLimits{MaxDepth: 1},
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentDepthLimitedChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "start the chain",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "parent final", result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 3)
	assert.Contains(t, toolNames(requests[0].Tools), toolNameLaunchAgent)
	assert.Contains(t, toolNames(requests[1].Tools), toolNameReadFile)
	assert.NotContains(t, toolNames(requests[1].Tools), toolNameLaunchAgent)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(
		t,
		runs,
		1,
		"the depth-limited child must be the only registered run",
	)
	assert.Equal(t, launchAgentChildName, runs[0].Name)
	assert.Equal(t, AgentRunStateCompleted, runs[0].Snapshot().State)
}

// The tool is absent at the depth limit, but a stale provider response can
// still contain an old tool call. The handler itself must reject that call
// before it creates another child run.
func TestLaunchAgentRejectsUnadvertisedCallPastDepthLimit(t *testing.T) {
	fixture := newLaunchAgentFixture(
		t,
		elelemtest.NewScriptedDriver(),
		launchAgentFixtureOptions{
			AgentLimits: AgentRunLimits{MaxDepth: 1},
			AgentFiles: map[string]string{
				launchAgentChildName: launchAgentChildAgentFile,
			},
		},
	)

	snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	_, err = fixture.runtime.launchAgent(
		contextWithAgentDepth(
			context.Background(),
			fixture.runtime.agentLimits.MaxDepth,
		),
		&launchAgentDeps{
			snapshot:  snapshot,
			sessionID: uuid.New(),
		},
		launchAgentInput{
			Task:  "delegate",
			Agent: launchAgentChildName,
		},
	)
	require.ErrorIs(t, err, ErrAgentDepthExceeded)

	query := repositories.Use(fixture.handle.GormDB)
	agentRunCount, err := query.AgentRun.WithContext(context.Background()).Count()
	require.NoError(t, err)
	assert.Zero(t, agentRunCount)
}

func toolNames(tools []elelem.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	return names
}

// A low MaxChildTurns bound must withhold tools on the child's final round,
// proving the bound is threaded into the child's own request rather than
// inherited from the parent turn's larger MaxToolRounds.
func TestLaunchAgentMaxChildTurnsWithholdsToolsOnLastRound(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "say hi",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.Text("child says hi"),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentLimits: AgentRunLimits{MaxChildTurns: 1},
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	requests := driver.Requests()
	require.Len(t, requests, 3)
	childRequest := requests[1]
	assert.Empty(
		t,
		childRequest.Tools,
		"max child turns of 1 must withhold tools on the only round",
	)

	parentFirstRequest := requests[0]
	assert.NotEmpty(
		t,
		parentFirstRequest.Tools,
		"the parent's own round must still offer its full tool set",
	)
}

// A child's own tool call can fail without failing the child's run: the
// child sees the failure as an ordinary tool result and still returns a
// final answer.
func TestLaunchAgentChildToolFailureStaysVisibleAndRunCompletes(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "read a missing file",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.ToolCall(
			"call_read",
			toolNameReadFile,
			runtimeToolArguments(t, "absent.txt"),
		),
		elelemtest.Text("the file is missing"),
		elelemtest.Text("done"),
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
	require.NoError(t, err, "a child tool failure must not fail the parent turn")
	assert.Equal(t, "done", result.Text)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	output := decodeLaunchAgentOutput(t, messages.Items[2].Content)
	assert.Equal(t, "the file is missing", output.Response)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 1)
	assert.Equal(
		t,
		AgentRunStateCompleted,
		runs[0].Snapshot().State,
		"a failed child tool call must not mark the run itself failed",
	)

	runEvents, _, _ := runs[0].ReadEvents(0, 0)
	found := false

	for _, event := range runEvents {
		if event.Type != EventTypeAgentRunToolResult {
			continue
		}

		payload := toolResultPayload{}
		require.NoError(t, json.Unmarshal(event.Payload, &payload))

		if payload.IsError {
			found = true
		}
	}

	assert.True(
		t,
		found,
		"the run's own event buffer must record the failed tool call",
	)
}

// A caller cannot switch the startup workspace session by supplying a session
// ID. Child runs remain available to every later turn in that workspace.
func TestLaunchAgentRunRemainsInStartupWorkspaceSession(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "say hi",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.Text("child says hi"),
		elelemtest.Text("done"),
		elelemtest.Text("unrelated session reply"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	first, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	registryA, err := fixture.runtime.sessionAgentRuns(first.SessionID)
	require.NoError(t, err)

	runs := registryA.List()
	require.Len(t, runs, 1)

	second, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &first.SessionID,
		Message:   "an unrelated session",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	require.Equal(t, first.SessionID, second.SessionID)

	registryB, err := fixture.runtime.sessionAgentRuns(second.SessionID)
	require.NoError(t, err)

	got, found := registryB.Get(runs[0].ID)
	assert.True(
		t,
		found,
		"a child run from the startup workspace must remain visible",
	)
	assert.Equal(t, runs[0].ID, got.ID)
}

// finishAgentRun's own decision logic, exercised directly: a single run's
// dedicated cancellation returns a typed success and leaves the parent
// context untouched, while the parent context itself being done propagates
// as a real, turn-ending error. Both record the run as cancelled. This is
// the precise new branch launch_agent adds; the underlying context
// cascade (a parent turn cancelling its whole child tree) is plain
// context.WithCancel behavior, already covered directly against
// AgentRunRegistry.Start in agentrun_test.go.
func TestFinishAgentRunDistinguishesSingleRunFromParentCancellation(t *testing.T) {
	driver := elelemtest.NewScriptedDriver()
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})

	t.Run("single run cancellation leaves the parent turn alive", func(t *testing.T) {
		parentCtx := context.Background()
		registry, run, _, sink := startDurableAgentRun(parentCtx, t, fixture)

		runErr := &wrappedCancelError{}
		output, err := fixture.runtime.finishAgentRun(
			parentCtx, registry, sink, run, nil, runErr,
		)
		require.NoError(t, err)
		assert.True(t, output.Cancelled)
		assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)
	})

	t.Run("parent cancellation propagates and cancels the child tree", func(t *testing.T) {
		startCtx, cancel := context.WithCancel(context.Background())
		registry, run, runCtx, sink := startDurableAgentRun(startCtx, t, fixture)

		cancel()
		assert.Error(t, runCtx.Err(), "parent cancellation must reach child work")

		runErr := &wrappedCancelError{}
		_, err := fixture.runtime.finishAgentRun(
			startCtx, registry, sink, run, nil, runErr,
		)
		require.Error(t, err)
		assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)
	})
}

func startDurableAgentRun(
	parentCtx context.Context,
	t *testing.T,
	fixture runtimeFixture,
) (*AgentRunRegistry, *AgentRun, context.Context, *agentRunSink) {
	t.Helper()

	sessionID := uuid.New()
	_, err := fixture.store.CreateOrResume(
		context.Background(),
		&sessionID,
		session.OpenSessionOptions{
			RootAgent: runtimeTestAgentName,
			ModelID:   runtimeTestModelReference,
		},
	)
	require.NoError(t, err)

	lease, err := fixture.store.AcquireTurn(
		context.Background(),
		sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: fixture.workspace,
			Messages: []session.MessageInput{{
				Role:    models.MessageRoleUser,
				Content: "start child",
			}},
		},
	)
	require.NoError(t, err)

	registry, err := fixture.runtime.sessionAgentRuns(sessionID)
	require.NoError(t, err)

	run, runCtx, err := registry.Start(parentCtx, StartAgentRunInput{
		ID:           uuid.New(),
		ParentTurnID: lease.TurnID,
		RequestID:    uuid.New(),
		Name:         "child",
		Definition:   AgentRunDefinitionStored,
		Depth:        1,
	}, func(run *AgentRun) error {
		_, createErr := fixture.store.CreateAgentRun(
			context.Background(),
			sessionID,
			session.StartAgentRunInput{
				ID:               run.ID,
				ParentTurnID:     run.ParentTurnID,
				ParentToolCallID: run.ParentToolCallID,
				RequestID:        run.RequestID,
				Name:             run.Name,
				Definition:       models.AgentRunDefinitionStored,
				Depth:            int64(run.Depth),
				Workspace:        fixture.workspace,
				ModelReference:   runtimeTestModelReference,
				ModelID:          runtimeTestModelID,
				Task:             "child task",
				Instructions:     "child instructions",
				AllowedToolsJSON: "[]",
				SystemPrompt:     "child system prompt",
				StartedAt:        run.StartedAt,
			},
		)

		return createErr
	})
	require.NoError(t, err)

	return registry, run, runCtx, newAgentRunSink(
		fixture.store,
		sessionID,
		run,
		nil,
		runtimeTestModelReference,
	)
}

// wrappedCancelError satisfies errors.Is(err, context.Canceled) without needing a
// real cancelled context.Context to construct, since finishAgentRun's
// decision reads parentCtx.Err() separately from the error value itself.
type wrappedCancelError struct{}

func (w *wrappedCancelError) Error() string { return context.Canceled.Error() }

func (w *wrappedCancelError) Is(target error) bool { return target == context.Canceled }
