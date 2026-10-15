//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	agentRunsPath = sessionPath + "/agents"

	childAgentsWorkspacePrefix = "childagents-"
	childAgentsCoreAgent       = "core:general-purpose"

	childAskMessage  = "delegate a question to a child"
	childAskTask     = "ask the user which branch to use"
	childAskQuestion = "which branch?"
	childAskAnswer   = "use the main branch"
	childAskDone     = "the child used the answer"
	childAskRootDone = "the root delegated"

	childStopMessage = "start a child that waits"
	childStopTask    = "wait until someone stops you"
	childStopRoot    = "the root launched a waiting child"

	childAgentsWaitBound   = "60s"
	childAgentsPollTimeout = 90 * time.Second
	childAgentsPollEvery   = 100 * time.Millisecond

	childAgentsErrorFinished = "AGENT_FINISHED"
)

// A child asks a question and waits. The API lists it as waiting with its
// question, a person answers it through the WebSocket by naming the child,
// and the child finishes with the answer in its conversation. A message to the
// finished child is refused as AGENT_FINISHED.
func TestAPIChildAgentAsksAndTheUserAnswersIt(t *testing.T) {
	workspace := openFreshWorkspace(t, childAgentsWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: childAskMessage,
		Calls: []testinfra.ScriptedToolCall{
			{
				Name: "launchAgent",
				Arguments: map[string]any{
					"agent": childAgentsCoreAgent,
					"task":  childAskTask,
				},
			},
			{
				Name:      "waitMessages",
				Arguments: map[string]any{"timeout": childAgentsWaitBound},
			},
		},
		FinalAnswer: childAskRootDone,
		Children: []testinfra.ScriptedToolTurn{{
			UserMessage: childAskTask,
			Calls: []testinfra.ScriptedToolCall{{
				Name:      "ask",
				Arguments: map[string]any{"question": childAskQuestion},
			}},
			FinalAnswer: childAskDone,
		}},
	})

	before := len(integrationInfra.ProviderRequests())

	sendSessionMessage(t, connection, workspace.sessionID, childAskMessage)
	readFramesUntilTurnCompleted(t, connection)

	waiting := awaitAgentRuns(t, workspace.sessionID, "?state=waiting", 1)
	child := waiting[0]
	assert.True(t, child.Waiting)
	require.NotNil(t, child.Question)
	assert.Equal(t, childAskQuestion, *child.Question)

	topLevel := listAgentRuns(t, workspace.sessionID, "?parentAgentRunId=root")
	require.Len(t, topLevel, 1)
	assert.Equal(t, child.AgentRunId, topLevel[0].AgentRunId)

	sendToAgentRun(t, connection, workspace.sessionID, child.AgentRunId)

	finished := awaitAgentRunState(
		t,
		workspace.sessionID,
		child.AgentRunId,
		api.AgentRunStateCompleted,
	)
	assert.False(t, finished.Waiting)
	assert.Equal(t, childAskDone, finished.ResponseText)

	childRequests := 0

	for _, request := range providerRequestsSince(before) {
		if !strings.Contains(request[0].Content, variantTestAgentBody) {
			continue
		}

		conversation := providerConversationText(request)

		childRequests++

		if childRequests == 2 {
			assert.Contains(
				t,
				conversation,
				"<message from=\"user\">\n"+childAskAnswer+"\n</message>",
				"the answer reaches the child tagged as the user's",
			)
		}
	}

	assert.Equal(t, 2, childRequests)

	require.NoError(t, writeAPIWebSocketSessionMessage(
		connection,
		workspace.sessionID,
		map[string]string{
			"message": childAskAnswer,
			"to":      child.AgentRunId.String(),
		},
	))

	failure := awaitAPIWebSocketFailure(t, connection)
	assert.Equal(t, childAgentsErrorFinished, failure.Code)
}

func TestAPIChildAgentRecoversAfterItsWorkerCrashes(t *testing.T) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		logs, err := integrationInfra.AppLogs(ctx)
		require.NoError(t, err)
		t.Logf("recovery failure container logs: %s", logs)
	})
	const nested = "recovery-child"
	const marker = "Apply the recovery directory instructions."
	const task = "enter the recovery directory and ask a question"
	workspace := openFreshWorkspace(t, childAgentsWorkspacePrefix, map[string]string{
		path.Join(nested, "AGENTS.md"): marker,
	})
	pidFile := path.Join(testinfra.ContainerWorkingDirectory, "recovery-worker-"+workspace.sessionID.String()+".pid")
	connection := openSessionConnection(t, workspace.sessionID)
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: childAskMessage,
		Calls:       []testinfra.ScriptedToolCall{{Name: "launchAgent", Arguments: map[string]any{"agent": childAgentsCoreAgent, "task": task}}},
		FinalAnswer: childAskRootDone,
		Children: []testinfra.ScriptedToolTurn{{
			UserMessage: task,
			Calls: []testinfra.ScriptedToolCall{
				{Name: "changeDirectory", Arguments: map[string]any{"directory": nested}},
				{Name: "runCommand", Arguments: map[string]any{
					"command":     `printf '%s' "$PPID" > "$RECOVERY_FIXTURE_PID_PATH"`,
					"purpose":     "record this test worker PID",
					"environment": map[string]string{"RECOVERY_FIXTURE_PID_PATH": pidFile},
				}},
				{Name: "ask", Arguments: map[string]any{"question": childAskQuestion}},
			},
			FinalAnswer: childAskDone,
		}},
	})
	sendSessionMessage(t, connection, workspace.sessionID, childAskMessage)
	readFramesUntilTurnCompleted(t, connection)
	child := awaitAgentRuns(t, workspace.sessionID, "?state=waiting", 1)[0]
	assert.Equal(t, path.Join(workspace.directory, nested), child.Workspace)
	before := len(integrationInfra.ProviderRequests())
	crashFixtureWorker(t, pidFile)
	require.NotNil(t, child.WorkerGenerationId)
	require.Eventually(t, func() bool {
		response := apiRequest(t, http.MethodGet, sessionPath+"/workers", nil,
			withHeader(authenticatedHeaders(), headerSessionID, workspace.sessionID.String()))
		requireAPIStatus(t, response, http.StatusOK)
		page := decodeResponse[api.WorkerGenerationPage](t, response)
		for _, generation := range page.Items {
			if generation.Id.String() == *child.WorkerGenerationId {
				return generation.State == api.WorkerGenerationStateFailed
			}
		}

		return false
	}, childAgentsPollTimeout, childAgentsPollEvery)
	sendToAgentRun(t, connection, workspace.sessionID, child.AgentRunId)
	keepChildAgentConnectionReading(t, connection)
	finished := awaitAgentRunState(t, workspace.sessionID, child.AgentRunId, api.AgentRunStateCompleted)
	assert.Equal(t, childAskDone, finished.ResponseText)
	assert.Equal(t, child.StartedAt, finished.StartedAt)
	assert.Equal(t, child.ParentTurnId, finished.ParentTurnId)
	assert.Equal(t, child.Name, finished.Name)
	childRequests := 0
	for _, request := range providerRequestsSince(before) {
		conversation := providerConversationText(request)
		if !strings.Contains(conversation, task) {
			continue
		}
		childRequests++
		assert.Contains(t, request[0].Content, marker)
		assert.Contains(t, conversation, childAskAnswer)
	}
	assert.Equal(t, 1, childRequests)
}

func TestAPIChildAgentTreeRecoversAfterItsWorkerCrashes(t *testing.T) {
	assertChildAgentTreeCrashRecovery(t, false)
}

func TestAPIChildAgentTreeRecoversAfterItsControllerCrashes(t *testing.T) {
	assertChildAgentTreeCrashRecovery(t, true)
}

func assertChildAgentTreeCrashRecovery(t *testing.T, crashController bool) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), requestTimeout)
			defer cancel()
			logs, err := integrationInfra.AppLogs(ctx)
			require.NoError(t, err)
			t.Logf("tree recovery container logs: %s", logs)
		}
	})
	const message = "launch the crash recovery tree"
	const marker = "Retain these tree directory instructions."
	children := map[string][]string{
		"branchA": {"leafA", "leafB", "leafC"},
		"branchB": {"leafD"}, "branchC": nil,
		"leafA": nil, "leafB": nil, "leafC": nil, "leafD": nil,
	}
	files := make(map[string]string, len(children))
	for name := range children {
		files[path.Join(name, "AGENTS.md")] = marker
	}
	workspace := openFreshWorkspace(t, childAgentsWorkspacePrefix, files)
	pidFile := path.Join(testinfra.ContainerWorkingDirectory, "tree-worker-"+workspace.sessionID.String()+".pid")
	script := testinfra.ScriptedToolTurn{
		UserMessage: message, FinalAnswer: "tree root finished",
		Calls: crashTreeLaunchCalls([]string{"branchA", "branchB", "branchC"}),
	}
	for name, descendants := range children {
		calls := crashTreeLaunchCalls(descendants)
		calls = append(calls,
			testinfra.ScriptedToolCall{Name: "changeDirectory", Arguments: map[string]any{
				"directory": path.Join(workspace.directory, name),
			}},
			testinfra.ScriptedToolCall{Name: "runCommand", Arguments: map[string]any{
				"command": `printf '%s\n' completed >> "$TREE_EFFECT_PATH" && printf '%s' "$PPID" > "$TREE_WORKER_PID_PATH"`,
				"purpose": "record a completed effect and this fixture's worker",
				"environment": map[string]string{
					"TREE_WORKER_PID_PATH": pidFile,
					"TREE_EFFECT_PATH":     crashTreeEffectPath(workspace.sessionID, name),
				},
			}},
			testinfra.ScriptedToolCall{Name: "waitMessages", Arguments: map[string]any{"timeout": "1h"}},
		)
		script.Children = append(script.Children, testinfra.ScriptedToolTurn{
			UserMessage: name, Calls: calls, FinalAnswer: "finished " + name,
		})
	}
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(script)
	connection := openSessionConnection(t, workspace.sessionID)
	sendSessionMessage(t, connection, workspace.sessionID, message)
	readFramesUntilTurnCompleted(t, connection)
	runs := awaitAgentRuns(t, workspace.sessionID, "?state=running", len(children))
	byName := make(map[string]api.AgentRun, len(runs))
	for _, run := range runs {
		awaitCrashTreeWait(t, workspace.sessionID, run.AgentRunId)
	}
	for _, run := range listAgentRuns(t, workspace.sessionID, "") {
		byName[run.Task] = run
		assert.Equal(t, path.Join(workspace.directory, run.Task), run.Workspace)
		assert.Equal(t, "completed\n", readContainerFile(t, crashTreeEffectPath(workspace.sessionID, run.Task)))
	}
	for parent, names := range children {
		for _, name := range names {
			run := byName[name]
			require.NotNil(t, run.ParentAgentRunId)
			assert.Equal(t, byName[parent].AgentRunId, *run.ParentAgentRunId)
			assert.EqualValues(t, 2, run.Depth)
		}
	}

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)
	sendToAgentRun(t, connection, workspace.sessionID, byName["leafA"].AgentRunId)
	select {
	case <-hold.Observed:
	case <-time.After(requestTimeout):
		t.Fatal("leaf final model request never reached the provider")
	}
	before := len(integrationInfra.ProviderRequests())
	if crashController {
		require.NoError(t, integrationInfra.CrashAndRestart(t.Context()))
		connection = openSessionConnection(t, workspace.sessionID)
	} else {
		crashFixtureWorker(t, pidFile)
	}
	select {
	case <-hold.Cancelled:
	case <-time.After(requestTimeout):
		t.Fatal("killed worker left its provider request active")
	}
	sendToAgentRun(t, connection, workspace.sessionID, byName["leafB"].AgentRunId)
	keepChildAgentConnectionReading(t, connection)
	for _, name := range []string{"leafA", "leafB"} {
		awaitAgentRunState(t, workspace.sessionID, byName[name].AgentRunId, api.AgentRunStateCompleted)
	}
	// Completion reports wake their parents. Only still-running runs need input.
	for _, name := range []string{"leafC", "leafD", "branchC", "branchA", "branchB"} {
		current := listAgentRuns(t, workspace.sessionID, "")
		for _, run := range current {
			if run.AgentRunId == byName[name].AgentRunId && run.State != api.AgentRunStateCompleted {
				require.NoError(t, writeAPIWebSocketSessionMessage(connection, workspace.sessionID,
					map[string]string{"message": childAskAnswer, "to": run.AgentRunId.String()}))
			}
		}
		awaitAgentRunState(t, workspace.sessionID, byName[name].AgentRunId, api.AgentRunStateCompleted)
	}
	finished := awaitAgentRuns(t, workspace.sessionID, "?state=completed", len(children))
	for _, run := range finished {
		original := byName[run.Task]
		assert.Equal(t, original.AgentRunId, run.AgentRunId)
		assert.Equal(t, original.ParentAgentRunId, run.ParentAgentRunId)
		assert.Equal(t, original.ParentTurnId, run.ParentTurnId)
		assert.Equal(t, original.StartedAt, run.StartedAt)
		assert.Equal(t, original.Workspace, run.Workspace)
		assert.NotEqual(t, original.WorkerGenerationId, run.WorkerGenerationId)
		assert.Equal(t, "finished "+run.Task, run.ResponseText)
		assert.Equal(t, "completed\n", readContainerFile(t, crashTreeEffectPath(workspace.sessionID, run.Task)))
		assert.Contains(t, run.SystemPrompt, marker)
	}
	leafRequests := 0
	for _, request := range providerRequestsSince(before) {
		if strings.Contains(providerConversationText(request), "\nleafA\n</message>") {
			leafRequests++
			assert.Contains(t, providerConversationText(request), childAskAnswer)
			assert.Contains(t, request[0].Content, marker)
		}
	}
	assert.Equal(t, 1, leafRequests, "only the interrupted model request retries")
}

func crashTreeLaunchCalls(names []string) []testinfra.ScriptedToolCall {
	calls := make([]testinfra.ScriptedToolCall, 0, len(names))
	for _, name := range names {
		calls = append(calls, testinfra.ScriptedToolCall{Name: "launchAgent", Arguments: map[string]any{
			"agentDefinition": map[string]string{"name": name, "instructions": "Complete the assigned tree task."},
			"task":            name,
		}})
	}

	return calls
}

func crashTreeEffectPath(sessionID uuid.UUID, name string) string {
	return path.Join(testinfra.ContainerWorkingDirectory, "tree-effect-"+sessionID.String()+"-"+name+".txt")
}

func awaitCrashTreeWait(t *testing.T, sessionID, runID uuid.UUID) {
	t.Helper()
	var page api.AgentRunEventPage
	require.Eventually(t, func() bool {
		response := apiRequest(t, http.MethodGet, agentRunsPath+"/"+runID.String()+"/events", nil,
			withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()))
		requireAPIStatus(t, response, http.StatusOK)
		page = decodeResponse[api.AgentRunEventPage](t, response)
		for _, event := range page.Events {
			if event.Type == agent.EventTypeAgentRunToolUse && event.Payload != nil && (*event.Payload)["name"] == "waitMessages" {
				return true
			}
		}

		return false
	}, childAgentsPollTimeout, childAgentsPollEvery)
	for _, event := range page.Events {
		if event.Type != agent.EventTypeAgentRunToolResult || event.Payload == nil || (*event.Payload)["name"] != "runCommand" {
			continue
		}
		payload := *event.Payload
		require.Equal(t, false, payload["isError"], "command result: %v", payload)
		content, ok := payload["content"].(string)
		require.True(t, ok)
		var result tools.RunCommandOutput
		require.NoError(t, json.Unmarshal([]byte(content), &result))
		require.Zero(t, result.ExitCode, "command output: %+v", result)
	}
}

func keepChildAgentConnectionReading(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	require.NoError(t, connection.SetReadDeadline(time.Time{}))
	stopped := make(chan struct{})
	var readErr error
	go func() {
		defer close(stopped)
		defer func() {
			if recover() != nil {
				readErr = ctxerrors.Wrap(commerr.ErrInvalidState,
					"child recovery websocket reader panicked")
			}
		}()
		for {
			if _, _, readErr = connection.ReadMessage(); readErr != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		require.NoError(t, connection.UnderlyingConn().SetReadDeadline(time.Now()))
		<-stopped
		var timeout net.Error
		require.ErrorAs(t, readErr, &timeout)
		assert.True(t, timeout.Timeout())
	})
}

func crashFixtureWorker(t *testing.T, pidFile string) {
	t.Helper()
	workerPID := readContainerFile(t, pidFile)
	pid, err := strconv.Atoi(workerPID)
	require.NoError(t, err)
	require.Greater(t, pid, 1)
	code, reader, err := integrationInfra.App.Exec(t.Context(), []string{"cat", "/proc/" + workerPID + "/cmdline"})
	require.NoError(t, err)
	require.Zero(t, code)
	command, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Contains(t, strings.Split(string(command), "\x00"), "worker")
	// The PID came from this test's child, and its command is the worker binary.
	code, reader, err = integrationInfra.App.Exec(t.Context(), []string{"/bin/bash", "-c", `kill -KILL "$1"`, "recovery-fixture", workerPID})
	require.NoError(t, err)
	_, err = io.ReadAll(reader)
	require.NoError(t, err)
	require.Zero(t, code)
}

// Stopping one child through the API reaches the worker it runs in. Before
// this, the endpoint only recorded the request and the child ran on.
func TestAPICancelStopsAChildRunningInTheWorker(t *testing.T) {
	workspace := openFreshWorkspace(t, childAgentsWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: childStopMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name: "launchAgent",
			Arguments: map[string]any{
				"agent": childAgentsCoreAgent,
				"task":  childStopTask,
			},
		}},
		FinalAnswer: childStopRoot,
		Children: []testinfra.ScriptedToolTurn{{
			UserMessage: childStopTask,
			Calls: []testinfra.ScriptedToolCall{{
				Name:      "waitMessages",
				Arguments: map[string]any{},
			}},
			FinalAnswer: childStopRoot,
		}},
	})

	sendSessionMessage(t, connection, workspace.sessionID, childStopMessage)
	readFramesUntilTurnCompleted(t, connection)

	running := awaitAgentRuns(t, workspace.sessionID, "?state=running", 1)
	child := running[0]

	response := apiRequest(
		t,
		http.MethodPost,
		agentRunsPath+"/"+child.AgentRunId.String()+"/cancel",
		nil,
		withHeader(
			authenticatedHeaders(),
			headerSessionID,
			workspace.sessionID.String(),
		),
	)
	requireAPIStatus(t, response, http.StatusAccepted)

	cancelled := awaitAgentRunState(
		t,
		workspace.sessionID,
		child.AgentRunId,
		api.AgentRunStateCancelled,
	)
	assert.True(t, cancelled.CancelRequested)
}

func listAgentRuns(
	t *testing.T,
	sessionID uuid.UUID,
	query string,
) []api.AgentRun {
	t.Helper()

	response := apiRequest(
		t,
		http.MethodGet,
		agentRunsPath+query,
		nil,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	requireAPIStatus(t, response, http.StatusOK)

	return decodeResponse[api.AgentRunPage](t, response).Agents
}

// awaitAgentRuns polls the agent list until it holds want runs.
func awaitAgentRuns(
	t *testing.T,
	sessionID uuid.UUID,
	query string,
	want int,
) []api.AgentRun {
	t.Helper()

	var runs []api.AgentRun

	require.Eventually(t, func() bool {
		runs = listAgentRuns(t, sessionID, query)

		return len(runs) == want
	}, childAgentsPollTimeout, childAgentsPollEvery)

	return runs
}

// awaitAgentRunState polls one run until it reaches state.
func awaitAgentRunState(
	t *testing.T,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	state api.AgentRunState,
) api.AgentRun {
	t.Helper()

	var run api.AgentRun

	require.Eventually(t, func() bool {
		response := apiRequest(
			t,
			http.MethodGet,
			agentRunsPath+"/"+agentRunID.String(),
			nil,
			withHeader(
				authenticatedHeaders(),
				headerSessionID,
				sessionID.String(),
			),
		)
		requireAPIStatus(t, response, http.StatusOK)

		run = decodeResponse[api.AgentRun](t, response)

		return run.State == state
	}, childAgentsPollTimeout, childAgentsPollEvery)

	return run
}

// sendToAgentRun answers a waiting child through the WebSocket and waits for
// the message to be accepted. A message to a child starts no turn, so it
// completes as queued.
func sendToAgentRun(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) {
	t.Helper()

	require.NoError(t, writeAPIWebSocketSessionMessage(
		connection,
		sessionID,
		map[string]string{
			"message": childAskAnswer,
			"to":      agentRunID.String(),
		},
	))

	result := awaitAPIWebSocketCompletion(t, connection, true)
	assert.True(t, result.result.Queued)
}
