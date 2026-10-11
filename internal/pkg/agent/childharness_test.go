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
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// These two are the instruction bodies of the fixture agent documents.
	// They are what tells a root prompt and a child prompt apart.
	childHarnessChildInstructions = "Do the child task."
	childHarnessRootInstructions  = "Follow the test agent rules."

	childHarnessWorkspaceRule = "Workspace rule: prefer the smallest change."
	childHarnessReadFileName  = "readable.txt"
	childHarnessReadContent   = "child readable content"
	childHarnessMissingFile   = "absent.txt"
	childHarnessWriteFileName = "blocked.txt"

	childHarnessReplayTask     = "review the readable file"
	childHarnessReplayCallID   = "call_child_replay"
	childHarnessReplayThinking = "child reasoning kept for replay"
	childHarnessReplayResponse = "child reviewed the file"

	// childHarnessScriptMode is owner read/write/execute. The hook command
	// runs as the test process and nothing else needs to reach it.
	childHarnessScriptMode = 0o700

	childHarnessGenericPreMark  = "HOOKMARK-generic-pre"
	childHarnessFilePreMark     = "HOOKMARK-file-pre"
	childHarnessFileSuccessMark = "HOOKMARK-file-success"
	childHarnessGenericPostMark = "HOOKMARK-generic-post"
	childHarnessGenericFailMark = "HOOKMARK-generic-failure"
	childHarnessFileFailMark    = "HOOKMARK-file-failure"
	childHarnessDenyReason      = "HOOKMARK-write-denied"

	childHarnessSessionStartMark    = "HOOKMARK-session-start"
	childHarnessPreUserMessageMark  = "HOOKMARK-pre-user-message"
	childHarnessPostUserMessageMark = "HOOKMARK-post-user-message"
	childHarnessTurnStartMark       = "HOOKMARK-turn-start"
	childHarnessTurnStopMark        = "HOOKMARK-turn-stop"
)

// writeChildHarnessHooks installs a hooks document in the configuration
// layer. The runtime resolves the harness per turn, so writing it after the fixture
// is built is what that turn sees.
func writeChildHarnessHooks(
	t *testing.T,
	fixture runtimeFixture,
	document string,
) {
	t.Helper()

	writeRuntimeFile(
		t,
		filepath.Join(fixture.configDirectory, ".agents", "hooks.yaml"),
		document,
	)
}

// childHarnessFixture builds a launch-agent fixture whose workspace carries a
// distinctive always-on rule, so a prompt assertion can tell resolved
// workspace rules apart from the base prompt and the child's instructions.
func childHarnessFixture(t *testing.T, driver elelem.Driver) runtimeFixture {
	t.Helper()

	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, "AGENTS.md"),
		childHarnessWorkspaceRule,
	)

	return fixture
}

// launchChildDriver scripts a parent that delegates once and waits, a child
// that answers with childTurns, then the parent's closing message.
func launchChildDriver(
	t *testing.T,
	childTurns ...elelemtest.Turn,
) *conversationDriver {
	t.Helper()

	return newConversationDriver(map[string]*elelemtest.ScriptedDriver{
		rootConversation: elelemtest.NewScriptedDriver(launchAndWaitTurns(
			t,
			launchAgentCallID,
			launchAgentInput{
				Task:  "do the child task",
				Agent: launchAgentChildName,
			},
			"parent done",
		)...),
		runtimeTestAgentName: elelemtest.NewScriptedDriver(childTurns...),
	}).after(runtimeTestAgentName, rootConversation, 2)
}

// requestSystemPrompt returns the system message one driver request carried.
func requestSystemPrompt(request elelem.DriverRequest) string {
	for _, message := range request.Messages {
		if message.Role == elelem.RoleSystem {
			return message.Text()
		}
	}

	return ""
}

// splitChildRequests returns the child conversation's driver requests and
// the root turn's. Each request's system prompt is checked too, so a request
// routed to the wrong script fails here.
func splitChildRequests(
	t *testing.T,
	driver *conversationDriver,
) ([]elelem.DriverRequest, []elelem.DriverRequest) {
	t.Helper()

	child := driver.requests(runtimeTestAgentName)
	root := driver.requests(rootConversation)

	require.NotEmpty(t, child, "the child never reached the model")
	require.NotEmpty(t, root, "the root turn never reached the model")

	for _, request := range child {
		require.Contains(
			t,
			requestSystemPrompt(request),
			childHarnessChildInstructions,
		)
	}

	for _, request := range root {
		require.NotContains(
			t,
			requestSystemPrompt(request),
			childHarnessChildInstructions,
		)
	}

	return child, root
}

// conversationText joins every message a set of requests carried, so a test
// can assert on hook injections without depending on their position.
func conversationText(requests []elelem.DriverRequest) string {
	parts := make([]string, 0, len(requests))

	for _, request := range requests {
		parts = append(parts, requestSystemPrompt(request))

		for _, message := range request.Messages {
			parts = append(parts, message.Text())
		}
	}

	return strings.Join(parts, "\n")
}

// runChildLaunchTurn runs one root turn that delegates to a child.
func runChildLaunchTurn(
	t *testing.T,
	fixture runtimeFixture,
) *TurnResult {
	t.Helper()

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	return result
}

// A child is launched by a tool call inside a parent turn, so nothing rebuilds
// the deployment prompt for it. It must still receive the base system
// instructions, because those describe how Peen expects its tools to be used.
// A child without them holds the same tools under different rules.
//
// The prompt is asserted on what the model actually received and on what
// replay stored, because those are the two places the defect was invisible.
func TestLaunchAgentChildPromptCarriesBaseSystemInstructions(t *testing.T) {
	driver := launchChildDriver(t, elelemtest.Text("child answered"))
	fixture := childHarnessFixture(t, driver)

	result := runChildLaunchTurn(t, fixture)
	require.Equal(t, "parent done", result.Text)

	childRequests, rootRequests := splitChildRequests(t, driver)
	childPrompt := requestSystemPrompt(childRequests[0])

	assert.Contains(
		t,
		childPrompt,
		defaultSystemPrompt,
		"the child must receive the Peen base system instructions",
	)
	assert.Contains(
		t,
		childPrompt,
		childHarnessWorkspaceRule,
		"the child keeps the resolved always-on workspace rules",
	)
	assert.Contains(
		t,
		childPrompt,
		childHarnessChildInstructions,
		"the child receives its own agent instructions",
	)
	assert.Contains(t, childPrompt, workspaceMetadataLead)
	assert.Contains(t, childPrompt, fixture.workspace)
	assert.Contains(t, childPrompt, runtimeContextHeader)

	// The child replaces the root agent block rather than adding to it. A
	// child carrying the parent's agent definition would answer as the root
	// agent while reporting itself as the child.
	assert.NotContains(
		t,
		childPrompt,
		childHarnessRootInstructions,
		"the root agent definition must not reach a child prompt",
	)

	rootPrompt := requestSystemPrompt(rootRequests[0])
	assert.Contains(t, rootPrompt, defaultSystemPrompt)
	assert.Contains(
		t,
		rootPrompt,
		childHarnessRootInstructions,
		"the root turn still carries its own agent definition",
	)

	// Replay has to show the prompt the child actually ran under.
	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 1)

	query := repositories.Use(fixture.handle.GormDB)
	storedRun, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.ID.Eq(runs[0].ID)).
		First()
	require.NoError(t, err)
	assert.Equal(
		t,
		childPrompt,
		storedRun.SystemPrompt,
		"the stored child prompt must be the prompt the model received",
	)
}

// Replay is the only record of what a child did once its parent turn is over,
// so every field a reader needs to reconstruct the run has to survive: who
// launched it, which worker generation and model ran it, what it was asked,
// which tools it held, what it answered, what it was thinking, and how it
// ended. The durable event stream has to carry the same run identity.
//
// The child here is restricted to readFile and produces both thinking and
// text, so the tools, thinking, and response fields are all non-empty rather
// than trivially equal to their zero values.
func TestLaunchAgentChildRunIsFullyReplayable(t *testing.T) {
	ctx := context.Background()
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task:  childHarnessReplayTask,
			Agent: launchAgentChildName,
		},
		elelemtest.ToolCall(
			childHarnessReplayCallID,
			toolNameReadFile,
			runtimeToolArguments(t, childHarnessReadFileName),
		),
		elelemtest.Thinking(
			childHarnessReplayThinking,
			childHarnessReplayResponse,
		),
	)

	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentRestrictedChildAgentFile,
		},
	})
	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, childHarnessReadFileName),
		childHarnessReadContent,
	)

	result := runChildLaunchTurn(t, fixture)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 1)

	query := repositories.Use(fixture.handle.GormDB)
	stored, err := query.AgentRun.WithContext(ctx).
		Where(query.AgentRun.ID.Eq(runs[0].ID)).
		First()
	require.NoError(t, err)

	turn, err := query.Turn.WithContext(ctx).
		Where(query.Turn.ID.Eq(runs[0].ParentTurnID)).
		First()
	require.NoError(t, err)

	// Who launched it.
	assert.Equal(t, result.SessionID, stored.SessionID)
	assert.Equal(t, turn.ID, stored.ParentTurnID, "parent turn")
	assert.Nil(
		t,
		stored.ParentAgentRunID,
		"a depth-1 child has no direct parent agent run",
	)
	assert.Equal(
		t,
		launchAgentCallID,
		stored.ParentToolCallID,
		"parent tool call",
	)
	assert.Equal(
		t,
		turn.WorkerGenerationID,
		stored.WorkerGenerationID,
		"worker generation",
	)

	// What ran, and under what.
	assert.Equal(t, runtimeTestModelReference, stored.ModelReference)
	assert.Equal(t, runtimeTestModelID, stored.ModelID)
	assert.Equal(t, childHarnessReplayTask, stored.Task, "task")
	assert.Equal(
		t,
		`["readFile"]`,
		stored.AllowedToolsJSON,
		"the child's restricted tool set is durable",
	)
	assert.NotEmpty(t, stored.SystemPrompt)

	// What came back.
	assert.Equal(t, childHarnessReplayResponse, stored.ResponseText, "response")
	assert.Equal(
		t,
		childHarnessReplayThinking,
		stored.ResponseThinking,
		"thinking",
	)
	assert.Equal(
		t,
		models.AgentRunStateCompleted,
		stored.State,
		"terminal state",
	)
	assert.NotNil(t, stored.EndedAt)
	assert.NotEmpty(t, stored.ResponseMessagesJSON)

	// The durable event stream carries the same run identity and the child's
	// own tool activity, which is what a reader replays.
	page, err := fixture.store.ListAgentRunEvents(
		ctx,
		result.SessionID,
		stored.ID,
		session.ListAgentRunEventsOptions{},
	)
	require.NoError(t, err)
	require.NotEmpty(t, page.Items, "the child recorded no durable events")
	require.NotNil(t, page.Run)
	assert.Equal(t, stored.ID, page.Run.ID)

	streamTypes := make([]string, 0, len(page.Items))

	for _, event := range page.Items {
		assert.Equal(t, stored.ID, event.AgentRunID, "event run identity")
		assert.Equal(t, result.SessionID, event.SessionID)

		streamTypes = append(streamTypes, event.EventType)
	}

	assert.Contains(t, streamTypes, EventTypeAgentRunToolUse)
	assert.Contains(t, streamTypes, EventTypeAgentRunToolResult)
	assert.Equal(
		t,
		int64(len(page.Items)),
		stored.EventCount,
		"the stored event count must match the durable stream",
	)
}

// A child runs host tools, so it runs the same mechanical tool hooks the root
// does: the generic pre and post events plus the file-specific pre and success
// events for the tool it called.
func TestLaunchAgentChildRunsGenericAndFileToolHooks(t *testing.T) {
	driver := launchChildDriver(
		t,
		elelemtest.ToolCall(
			"call_child_read",
			toolNameReadFile,
			runtimeToolArguments(t, childHarnessReadFileName),
		),
		elelemtest.Text("child read the file"),
	)
	fixture := childHarnessFixture(t, driver)

	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, childHarnessReadFileName),
		childHarnessReadContent,
	)
	writeChildHarnessHooks(t, fixture, `version: 1
preToolUse:
  - name: generic-pre
    actions:
      - type: inject
        message: `+childHarnessGenericPreMark+`
preReadFile:
  - name: file-pre
    actions:
      - type: inject
        message: `+childHarnessFilePreMark+`
postReadFile:
  - name: file-success
    actions:
      - type: inject
        message: `+childHarnessFileSuccessMark+`
postToolUse:
  - name: generic-post
    actions:
      - type: inject
        message: `+childHarnessGenericPostMark+`
`)

	runChildLaunchTurn(t, fixture)

	childRequests, _ := splitChildRequests(t, driver)
	conversation := conversationText(childRequests)

	for _, mark := range []string{
		childHarnessGenericPreMark,
		childHarnessFilePreMark,
		childHarnessFileSuccessMark,
		childHarnessGenericPostMark,
	} {
		assert.Contains(
			t,
			conversation,
			mark,
			"the child must run the %s hook", mark,
		)
	}
}

// A command hook reads its invocation as JSON on stdin, so the request ID is
// only correlatable if the child's hook runtime carries the launching
// request's ID. The child run row records that same ID, so a hook reading
// uuid.Nil could not be joined to the request its own work is filed under.
//
// The root turn calls no file tool here, so the only invocation this hook can
// capture is the child's.
func TestLaunchAgentChildToolHookCommandReceivesRootRequestID(t *testing.T) {
	ctx := context.Background()
	driver := launchChildDriver(
		t,
		elelemtest.ToolCall(
			childHarnessReplayCallID,
			toolNameReadFile,
			runtimeToolArguments(t, childHarnessReadFileName),
		),
		elelemtest.Text("child read the file"),
	)
	fixture := childHarnessFixture(t, driver)

	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, childHarnessReadFileName),
		childHarnessReadContent,
	)

	// The script and its output live in the test's own temporary directory,
	// never in the workspace the agent can reach.
	scriptRoot := t.TempDir()
	invocationPath := filepath.Join(scriptRoot, "invocation.json")
	scriptPath := filepath.Join(scriptRoot, "capture-invocation.sh")

	encodedPath, err := json.Marshal(invocationPath)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(
		scriptPath,
		[]byte("#!/bin/sh\nexec cat > "+string(encodedPath)+"\n"),
		childHarnessScriptMode,
	))

	writeChildHarnessHooks(t, fixture, `version: 1
preReadFile:
  - name: capture-invocation
    actions:
      - name: capture
        type: command
        command: `+scriptPath+`
`)

	result := runChildLaunchTurn(t, fixture)

	captured, err := os.ReadFile(invocationPath)
	require.NoError(t, err, "the child tool hook command never ran")

	invocation := hooks.Invocation{}
	require.NoError(t, json.Unmarshal(captured, &invocation))

	query := repositories.Use(fixture.handle.GormDB)
	turn, err := query.Turn.WithContext(ctx).
		Where(query.Turn.SessionID.Eq(result.SessionID)).
		First()
	require.NoError(t, err)

	storedRun, err := query.AgentRun.WithContext(ctx).
		Where(query.AgentRun.ParentTurnID.Eq(turn.ID)).
		First()
	require.NoError(t, err)

	// The root turn's own durable row is the independent record of the
	// request. Comparing only against the child run row would agree even if
	// both sides carried the same wrong value, because both are written from
	// the launch deps.
	require.NotEqual(
		t,
		uuid.Nil,
		turn.RequestID,
		"the root turn must record a real request ID",
	)
	assert.Equal(
		t,
		turn.RequestID,
		invocation.RequestID,
		"the child hook invocation must carry the root request ID",
	)
	assert.Equal(
		t,
		turn.RequestID,
		storedRun.RequestID,
		"the durable child run is filed under the same request",
	)

	// The rest of the correlation identity travels with it.
	assert.Equal(t, result.SessionID, invocation.SessionID)
	assert.Equal(t, turn.ID, invocation.TurnID)
	require.NotNil(t, invocation.AgentRunID)
	assert.Equal(t, storedRun.ID, *invocation.AgentRunID)
}

// A failing child tool runs the generic failure event and the file-specific
// failure event. Without them a workspace could gate every successful write
// and still miss every rejected one.
func TestLaunchAgentChildRunsToolFailureHooks(t *testing.T) {
	driver := launchChildDriver(
		t,
		elelemtest.ToolCall(
			"call_child_missing",
			toolNameReadFile,
			runtimeToolArguments(t, childHarnessMissingFile),
		),
		elelemtest.Text("child could not read"),
	)
	fixture := childHarnessFixture(t, driver)

	writeChildHarnessHooks(t, fixture, `version: 1
toolUseFailure:
  - name: generic-failure
    actions:
      - type: inject
        message: `+childHarnessGenericFailMark+`
readFileFailure:
  - name: file-failure
    actions:
      - type: inject
        message: `+childHarnessFileFailMark+`
`)

	runChildLaunchTurn(t, fixture)

	childRequests, _ := splitChildRequests(t, driver)
	conversation := conversationText(childRequests)

	assert.Contains(t, conversation, childHarnessGenericFailMark)
	assert.Contains(t, conversation, childHarnessFileFailMark)
}

// A deny outcome stops the child's tool rather than annotating it. The
// filesystem is the proof: a denied write leaves no file behind.
func TestLaunchAgentChildToolHookDenyStopsTheTool(t *testing.T) {
	driver := launchChildDriver(
		t,
		elelemtest.ToolCall(
			"call_child_write",
			toolNameWriteFile,
			`{"path":"`+childHarnessWriteFileName+`","content":"nope"}`,
		),
		elelemtest.Text("child was denied"),
	)
	fixture := childHarnessFixture(t, driver)

	writeChildHarnessHooks(t, fixture, `version: 1
preWriteFile:
  - name: block-writes
    actions:
      - type: deny
        reason: `+childHarnessDenyReason+`
`)

	runChildLaunchTurn(t, fixture)

	childRequests, _ := splitChildRequests(t, driver)
	assert.Contains(
		t,
		conversationText(childRequests),
		childHarnessDenyReason,
		"the child must see why its tool was denied",
	)

	_, statErr := os.Stat(
		filepath.Join(fixture.workspace, childHarnessWriteFileName),
	)
	assert.True(
		t,
		os.IsNotExist(statErr),
		"a denied child write must not reach the workspace",
	)
}

// A child is neither a user message nor a new session, so the user-message and
// session lifecycle hooks must not fire for it.
//
// turnStart, turnStop, and turnCancelled are parent-turn lifecycle events
// rather than child-model-call events. They run from the turn's lease in
// startLease, completeLease, and the cancellation path, all of which key on
// the preparedTurn. A child is launched from inside a turn after turnStart
// already fired, and it may end before or after turnStop, so firing them per
// child would report turn starts and stops that no turn made.
//
// These events inject before the model, which lands them in the system prompt
// of whichever conversation ran them. The root's prompt therefore carries them
// and the child's must not.
func TestLaunchAgentChildDoesNotRunUserOrSessionLifecycleHooks(t *testing.T) {
	driver := launchChildDriver(t, elelemtest.Text("child answered"))
	fixture := childHarnessFixture(t, driver)

	writeChildHarnessHooks(t, fixture, `version: 1
sessionStart:
  - name: session-start
    actions:
      - type: inject
        message: `+childHarnessSessionStartMark+`
preUserMessage:
  - name: pre-user-message
    actions:
      - type: inject
        message: `+childHarnessPreUserMessageMark+`
postUserMessage:
  - name: post-user-message
    actions:
      - type: inject
        message: `+childHarnessPostUserMessageMark+`
turnStart:
  - name: turn-start
    actions:
      - type: inject
        message: `+childHarnessTurnStartMark+`
turnStop:
  - name: turn-stop
    actions:
      - type: inject
        message: `+childHarnessTurnStopMark+`
`)

	runChildLaunchTurn(t, fixture)

	childRequests, rootRequests := splitChildRequests(t, driver)
	childConversation := conversationText(childRequests)

	for _, mark := range []string{
		childHarnessSessionStartMark,
		childHarnessPreUserMessageMark,
		childHarnessPostUserMessageMark,
		childHarnessTurnStartMark,
		childHarnessTurnStopMark,
	} {
		assert.NotContains(
			t,
			childConversation,
			mark,
			"%s is a parent-turn event and must not reach a child", mark,
		)
	}

	// The same document does fire for the root turn, which is what proves the
	// child's silence comes from scoping rather than from hooks never loading.
	rootConversation := conversationText(rootRequests)
	assert.Contains(t, rootConversation, childHarnessSessionStartMark)
	assert.Contains(t, rootConversation, childHarnessPostUserMessageMark)
	assert.Contains(t, rootConversation, childHarnessTurnStartMark)
}
