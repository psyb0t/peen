//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Workflow task leases survive a crash and must expire before reclamation.
const executionCrashRecoveryWait = 3 * time.Minute

func TestAPIExecutionHistoryToolsInspectPausedWorkWithoutResuming(t *testing.T) {
	const oldTask = "retain this abandoned execution for inspection"
	const newTask = "inspect the earlier stopped task"
	workspace := openFreshWorkspace(t, "execution-history-", nil)
	connection := openSessionConnection(t, workspace.sessionID)
	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)
	sendSessionMessage(t, connection, workspace.sessionID, oldTask)
	select {
	case <-hold.Observed:
	case <-time.After(requestTimeout):
		t.Fatal("the abandoned execution did not reach its model request")
	}
	response := apiRequest(t, http.MethodPost, sessionCancelPath, nil,
		withHeader(authenticatedHeaders(), headerSessionID, workspace.sessionID.String()))
	requireAPIStatus(t, response, http.StatusAccepted)
	assert.True(t, decodeResponse[api.CancelResponse](t, response).CancelRequested)
	awaitAPIWebSocketFailure(t, connection)
	select {
	case <-hold.Cancelled:
	case <-time.After(requestTimeout):
		t.Fatal("Stop did not interrupt the abandoned model request")
	}
	arguments := func(part string) func([]string) map[string]any {
		return func(results []string) map[string]any {
			var page session.ExecutionPage
			target := uuid.Nil
			if len(results) > 0 {
				if err := json.Unmarshal([]byte(results[0]), &page); err == nil && len(page.Items) == 1 {
					target = page.Items[0].ID
				}
			}

			return map[string]any{"executionId": target, "part": part, "limit": 5}
		}
	}
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: newTask, FinalAnswer: "the paused work was inspected",
		Calls: []testinfra.ScriptedToolCall{
			{Name: "searchExecutions", Arguments: map[string]any{"text": oldTask, "state": "paused", "limit": 5}},
			{Name: "readExecution", ArgumentsFrom: arguments("execution")},
			{Name: "readExecution", ArgumentsFrom: arguments("events")},
		},
	})
	before := len(integrationInfra.ProviderRequests())
	sendSessionMessage(t, connection, workspace.sessionID, newTask)
	readFramesUntilTurnCompleted(t, connection)
	results := make(map[string]api.Message)
	for _, message := range collectAllMessages(t, workspace.sessionID) {
		if message.Role == api.MessageRoleTool && message.ToolCallId != nil {
			results[*message.ToolCallId] = message
			assert.False(t, message.IsError != nil && *message.IsError, "%s", message.Content)
		}
	}
	var page session.ExecutionPage
	require.NoError(t, json.Unmarshal([]byte(toolOutputText(t,
		results[testinfra.ScriptedCallID(0)].Content)), &page))
	require.Len(t, page.Items, 1)
	assert.Equal(t, oldTask, page.Items[0].Task)
	assert.Equal(t, "paused", string(page.Items[0].State))
	var saved struct {
		Execution struct {
			ID              uuid.UUID `json:"id"`
			State           string    `json:"state"`
			ControlRevision int64     `json:"controlRevision"`
		} `json:"execution"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolOutputText(t,
		results[testinfra.ScriptedCallID(1)].Content)), &saved))
	assert.Equal(t, page.Items[0].ID, saved.Execution.ID)
	assert.Equal(t, "paused", saved.Execution.State)
	assert.Equal(t, page.Items[0].ControlRevision, saved.Execution.ControlRevision)
	var events struct {
		Events session.ExecutionEventPage `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolOutputText(t,
		results[testinfra.ScriptedCallID(2)].Content)), &events))
	require.NotEmpty(t, events.Events.Items)
	assert.Equal(t, page.Items[0].ID, events.Events.Items[0].ExecutionID)
	assert.Len(t, providerRequestsSince(before), 4, "reading history must not restart the old model request")
}

func TestAPIExecutionResumeHandsOffAfterCurrentTurn(t *testing.T) {
	const oldTask = "retain this task until explicitly resumed"
	const newTask = "resume the retained task after finishing this request"
	workspace := openFreshWorkspace(t, "execution-resume-", nil)
	connection := openSessionConnection(t, workspace.sessionID)
	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)
	sendSessionMessage(t, connection, workspace.sessionID, oldTask)
	select {
	case <-hold.Observed:
	case <-time.After(requestTimeout):
		t.Fatal("original task did not reach the provider")
	}
	response := apiRequest(t, http.MethodPost, sessionCancelPath, nil,
		withHeader(authenticatedHeaders(), headerSessionID, workspace.sessionID.String()))
	requireAPIStatus(t, response, http.StatusAccepted)
	awaitAPIWebSocketFailure(t, connection)
	select {
	case <-hold.Cancelled:
	case <-time.After(requestTimeout):
		t.Fatal("original provider request did not stop")
	}
	arguments := func(results []string) map[string]any {
		var page session.ExecutionPage
		if len(results) == 0 || json.Unmarshal([]byte(results[0]), &page) != nil || len(page.Items) != 1 {
			return map[string]any{"executionId": uuid.Nil, "controlRevision": 0}
		}

		return map[string]any{"executionId": page.Items[0].ID, "controlRevision": page.Items[0].ControlRevision}
	}
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: newTask, FinalAnswer: "current request finished",
		Calls: []testinfra.ScriptedToolCall{
			{Name: "searchExecutions", Arguments: map[string]any{"text": oldTask, "state": "paused", "limit": 5}},
			{Name: "resumeExecution", ArgumentsFrom: arguments},
			{Name: "readExecution", ArgumentsFrom: func(results []string) map[string]any {
				input := arguments(results)

				return map[string]any{"executionId": input["executionId"]}
			}},
		},
	})
	before := len(integrationInfra.ProviderRequests())
	sendSessionMessage(t, connection, workspace.sessionID, newTask)
	readFramesUntilTurnCompleted(t, connection)
	results := make(map[string]api.Message)
	for _, message := range collectAllMessages(t, workspace.sessionID) {
		if message.Role == api.MessageRoleTool && message.ToolCallId != nil {
			results[*message.ToolCallId] = message
			assert.False(t, message.IsError != nil && *message.IsError, "%s", message.Content)
		}
	}
	var page session.ExecutionPage
	require.NoError(t, json.Unmarshal([]byte(toolOutputText(t,
		results[testinfra.ScriptedCallID(0)].Content)), &page))
	require.Len(t, page.Items, 1)
	original := page.Items[0]
	var resumed struct {
		ExecutionID uuid.UUID   `json:"executionId"`
		ResumedIDs  []uuid.UUID `json:"resumedIds"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolOutputText(t,
		results[testinfra.ScriptedCallID(1)].Content)), &resumed))
	assert.Equal(t, original.ID, resumed.ExecutionID)
	assert.Equal(t, []uuid.UUID{original.ID}, resumed.ResumedIDs)
	var inspected struct {
		Execution *models.Execution `json:"execution"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolOutputText(t,
		results[testinfra.ScriptedCallID(2)].Content)), &inspected))
	require.NotNil(t, inspected.Execution)
	assert.Contains(t, []models.ExecutionState{models.ExecutionStateQueued, models.ExecutionStateRunning}, inspected.Execution.State)
	assert.Equal(t, original.ControlRevision+1, inspected.Execution.ControlRevision)
	require.Eventually(t, func() bool {
		response := apiRequest(t, http.MethodGet, sessionPath+"/events?limit=100&order=desc", nil,
			withHeader(authenticatedHeaders(), headerSessionID, workspace.sessionID.String()))
		requireAPIStatus(t, response, http.StatusOK)
		events := decodeResponse[api.TranscriptEventPage](t, response)
		var originalSequence, currentSequence int64
		for _, event := range events.Events {
			if event.Type != apiTestWebSocketTurnCompleted {
				continue
			}
			if event.TurnId == original.RootTurnID {
				originalSequence = event.Sequence
			} else {
				currentSequence = event.Sequence
			}
		}

		return originalSequence > currentSequence && currentSequence > 0
	}, requestTimeout, 50*time.Millisecond)
	assert.Len(t, providerRequestsSince(before), 5, "the original model retries once after the current request's four rounds")
}

func TestAPIExecutionEffectCrashResumesBeforeQueuedMessage(t *testing.T) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(
			context.WithoutCancel(t.Context()), requestTimeout)
		defer cancel()
		logs, err := integrationInfra.AppLogs(ctx)
		require.NoError(t, err)
		t.Logf("effect recovery container logs: %s", logs)
	})
	const task = "retain this unacknowledged command effect across a crash"
	const inspectTask = "inspect the crashed command and its files"
	const command = `printf 'attempt\n' >> "$ATTEMPTS_PATH"; ` +
		`if test -f "$EFFECT_PATH"; then cat "$EFFECT_PATH"; ` +
		`else printf 'effect\n' > "$EFFECT_PATH"; sleep 3600; fi`
	workspace := openFreshWorkspace(t, "effect-crash-", nil)
	// Fixture directories are root-owned; the command writes in the runtime
	// account's writable temporary area.
	effectPath := path.Join(testinfra.ContainerWorkingDirectory,
		"crash-effect-"+uuid.NewString())
	attemptsPath := effectPath + ".attempts"
	connection := openSessionConnection(t, workspace.sessionID)
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: task, FinalAnswer: "the effect was acknowledged",
		Calls: []testinfra.ScriptedToolCall{{
			Name: "runCommand", Arguments: map[string]any{
				"command": command, "purpose": task, "timeout": "1h",
				"environment": map[string]string{
					"EFFECT_PATH": effectPath, "ATTEMPTS_PATH": attemptsPath,
				},
			},
		}},
	})
	sendSessionMessage(t, connection, workspace.sessionID, task)
	awaitFixtureCommand(t, "test", "-s", effectPath)
	require.Equal(t, "attempt\n", readContainerFile(t, attemptsPath))
	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: inspectTask, FinalAnswer: "inspected without retrying",
		Calls: []testinfra.ScriptedToolCall{
			{Name: "readFile", Arguments: map[string]any{"path": effectPath}},
			{Name: "readFile", Arguments: map[string]any{"path": attemptsPath}},
		},
	})
	require.NoError(t, integrationInfra.CrashAndRestart(t.Context()))
	connection = openSessionConnection(t, workspace.sessionID)
	sendSessionMessage(t, connection, workspace.sessionID, inspectTask)
	queued := awaitAPIWebSocketCompletion(t, connection, true)
	require.True(t, queued.result.Queued)
	hold.Release()
	require.Eventually(t, func() bool {
		messages := toolMessages(t, workspace.sessionID)

		return len(messages) == 3
	}, executionCrashRecoveryWait, backgroundJobEventPoll)
	messages := toolMessages(t, workspace.sessionID)
	require.Len(t, messages, 3)
	require.True(t, toolFailed(messages[0]), "%s", messages[0].Content)
	require.NotNil(t, messages[0].TurnId)
	assert.Contains(t, messages[0].Content, "outcome is unknown")
	for _, message := range messages[1:] {
		require.False(t, toolFailed(message), "%s", message.Content)
		assert.Greater(t, message.Sequence, messages[0].Sequence)
		assert.NotEqual(t, messages[0].TurnId, message.TurnId)
	}
	assert.Contains(t, messages[1].Content, "effect")
	assert.Contains(t, messages[2].Content, "attempt")
	require.Eventually(t, func() bool {
		response := apiRequest(t, http.MethodGet,
			sessionPath+"/events?limit=100&order=desc", nil,
			withHeader(authenticatedHeaders(), headerSessionID,
				workspace.sessionID.String()))
		requireAPIStatus(t, response, http.StatusOK)
		events := decodeResponse[api.TranscriptEventPage](t, response)
		var original, later, laterStarted int64
		for _, event := range events.Events {
			if event.Type == apiTestWebSocketTurnStarted &&
				event.TurnId != *messages[0].TurnId {
				laterStarted = event.Sequence
			}

			if event.Type != apiTestWebSocketTurnCompleted {
				continue
			}
			if event.TurnId == *messages[0].TurnId {
				original = event.Sequence
			} else {
				later = event.Sequence
			}
		}

		return original > 0 && laterStarted > original && later > laterStarted
	}, requestTimeout, backgroundJobEventPoll)
	assert.Equal(t, "attempt\n", readContainerFile(t, attemptsPath))
	assert.Equal(t, "effect\n", readContainerFile(t, effectPath))
}
