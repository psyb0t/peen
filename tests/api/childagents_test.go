//go:build integration

package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
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
