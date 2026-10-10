//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	jobToolsWorkspacePrefix = "jobtools-"
	jobToolsMessage         = "start a job and wait for it in several ways"
	jobToolsFinalAnswer     = "the job tools were exercised"
	jobToolsOutputLine      = "job tool output line"
	jobToolsCommand         = "sleep 2; echo '" + jobToolsOutputLine + "'"
	jobToolsPurpose         = "api job tool fixture"
	jobToolsShortWait       = "300ms"
	jobToolsLongWait        = "1m"
	jobToolsBadTimeout      = "soon"
	jobToolsBadTimeoutError = `timeout "soon" must be a Go duration such ` +
		`as 90s, 5m, or 1h30m: time: invalid duration "soon": validation failed`
	jobToolsOldArgumentError = `decode tool arguments: json: unknown field ` +
		`"timeoutSeconds"`
	jobToolsStateRunning    = "running"
	jobToolsStateExited     = "exited"

	jobNoticeWorkspacePrefix = "jobnotice-"
	jobNoticeMessage         = "start a short background job"
	jobNoticeFollowUp        = "what finished while you were away"
	jobNoticeFinalAnswer     = "the short job started"
	jobNoticeCommand         = "sleep 1"
	jobNoticePurpose         = "api job notice fixture"
	jobNoticeWait            = 30 * time.Second
	jobNoticePoll            = 200 * time.Millisecond

	agentToolsWorkspacePrefix = "launchagent-"
	agentToolsMessage         = "launch the general purpose agent"
	agentToolsAgent           = "core:general-purpose"
	agentToolsTask            = "summarize the plan with :core:planning"
	agentToolsFinalAnswer     = "the child agent answered"
	agentToolsTextPrefix      = "integration completion "
)

type jobToolsResult struct {
	JobID      string `json:"jobId"`
	Found      bool   `json:"found"`
	Running    bool   `json:"running"`
	State      string `json:"state"`
	ExitCode   *int   `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	Duration   string `json:"duration"`
	Stdout     string `json:"stdout"`
}

// wait_job waits for a real job up to its timeout: a short timeout reports it
// still running, a long one reports how it exited with its output. A timeout
// that is not a Go duration, and the retired timeoutSeconds argument, are
// refused as failed tool calls and run nothing.
func TestAPIWaitJobHonorsItsTimeoutThroughTheWorker(t *testing.T) {
	workspace := openFreshWorkspace(t, jobToolsWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	jobID := func(results []string) string {
		started := jobToolsResult{}
		require.NoError(t, json.Unmarshal([]byte(results[0]), &started))

		return started.JobID
	}

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: jobToolsMessage,
		Calls: []testinfra.ScriptedToolCall{
			{
				Name: "run_command",
				Arguments: map[string]any{
					"command":    jobToolsCommand,
					"purpose":    jobToolsPurpose,
					"background": true,
				},
			},
			{
				Name: "wait_job",
				ArgumentsFrom: func(results []string) map[string]any {
					return map[string]any{
						"jobId":   jobID(results),
						"timeout": jobToolsShortWait,
					}
				},
			},
			{
				Name: "wait_job",
				ArgumentsFrom: func(results []string) map[string]any {
					return map[string]any{
						"jobId":   jobID(results),
						"timeout": jobToolsLongWait,
					}
				},
			},
			{
				Name: "run_command",
				Arguments: map[string]any{
					"command": "true",
					"purpose": jobToolsPurpose,
					"timeout": jobToolsBadTimeout,
				},
			},
			{
				Name: "run_command",
				Arguments: map[string]any{
					"command":        "true",
					"purpose":        jobToolsPurpose,
					"timeoutSeconds": 5,
				},
			},
			{
				Name: "wait_job",
				ArgumentsFrom: func(results []string) map[string]any {
					return map[string]any{
						"jobId":   jobID(results),
						"timeout": jobToolsBadTimeout,
					}
				},
			},
			{
				Name: "wait_job",
				ArgumentsFrom: func(results []string) map[string]any {
					return map[string]any{
						"jobId":          jobID(results),
						"timeoutSeconds": 5,
					}
				},
			},
		},
		FinalAnswer: jobToolsFinalAnswer,
	})

	sendSessionMessage(t, connection, workspace.sessionID, jobToolsMessage)
	readFramesUntilTurnCompleted(t, connection)

	tools := toolMessages(t, workspace.sessionID)
	require.Len(t, tools, 7)

	for index, tool := range tools {
		assert.Equal(t, testinfra.ScriptedCallID(index), *tool.ToolCallId)
	}

	started := decodeJobToolsResult(t, tools[0])
	assert.NotEmpty(t, started.JobID)

	stillRunning := decodeJobToolsResult(t, tools[1])
	assert.True(t, stillRunning.Found)
	assert.True(t, stillRunning.Running)
	assert.Equal(t, jobToolsStateRunning, stillRunning.State)
	assert.Equal(t, started.JobID, stillRunning.JobID)

	finished := decodeJobToolsResult(t, tools[2])
	assert.True(t, finished.Found)
	assert.False(t, finished.Running)
	assert.Equal(t, jobToolsStateExited, finished.State)
	require.NotNil(t, finished.ExitCode)
	assert.Equal(t, 0, *finished.ExitCode)
	assert.Contains(t, finished.Stdout, jobToolsOutputLine)
	assert.GreaterOrEqual(t, finished.DurationMs, int64(1000))

	parsed, err := time.ParseDuration(finished.Duration)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(finished.DurationMs)*time.Millisecond, parsed)

	assert.True(t, toolFailed(tools[3]))
	assert.Equal(t, jobToolsBadTimeoutError, tools[3].Content)

	assert.True(t, toolFailed(tools[4]))
	assert.Equal(t, jobToolsOldArgumentError, tools[4].Content)

	assert.True(t, toolFailed(tools[5]))
	assert.Equal(t, jobToolsBadTimeoutError, tools[5].Content)

	assert.True(t, toolFailed(tools[6]))
	assert.Equal(t, jobToolsOldArgumentError, tools[6].Content)
}

// A job that ends after its run_command call returned publishes job.exited
// with its run time as durationMs and as a Go duration string, and the next
// turn receives that same data in its events message.
func TestAPIJobExitedNoticeCarriesTheDuration(t *testing.T) {
	workspace := openFreshWorkspace(t, jobNoticeWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: jobNoticeMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name: "run_command",
			Arguments: map[string]any{
				"command":    jobNoticeCommand,
				"purpose":    jobNoticePurpose,
				"background": true,
			},
		}},
		FinalAnswer: jobNoticeFinalAnswer,
	})

	sendSessionMessage(t, connection, workspace.sessionID, jobNoticeMessage)
	readFramesUntilTurnCompleted(t, connection)
	integrationInfra.DisableScriptedToolTurn()

	var exited api.SessionNotice

	require.Eventually(t, func() bool {
		return findNotice(t, workspace.sessionID, hostToolsJobExitedType, &exited)
	}, jobNoticeWait, jobNoticePoll, "the job published job.exited")

	require.NotNil(t, exited.Data)
	data := *exited.Data

	duration, isText := data["duration"].(string)
	require.True(t, isText, "duration is a string: %v", data)

	durationMs, isNumber := data["durationMs"].(float64)
	require.True(t, isNumber, "durationMs is a number: %v", data)

	parsed, err := time.ParseDuration(duration)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(durationMs)*time.Millisecond, parsed)
	assert.GreaterOrEqual(t, durationMs, float64(1000))

	before := len(integrationInfra.ProviderRequests())

	sendSessionMessage(t, connection, workspace.sessionID, jobNoticeFollowUp)
	readFramesUntilTurnCompleted(t, connection)

	requests := providerRequestsSince(before)
	require.Len(t, requests, 1)

	var events string

	for _, message := range nonSystemMessages(t, requests[0]) {
		if strings.HasPrefix(message.Content, handlerTestEventsPrefix) {
			events = message.Content
		}
	}

	require.NotEmpty(t, events, "the follow-up turn received the events message")
	assert.Contains(t, events, `"duration":"`+duration+`"`)
	assert.Contains(t, events, `"type":"job.exited"`)
}

// launch_agent through the worker: the child runs as the core agent the call
// names, its task's :name skill follows the task as its own message, and the
// parent receives the child's answer as the tool result.
func TestAPILaunchAgentRunsACoreAgentWithATaskSkill(t *testing.T) {
	workspace := openFreshWorkspace(t, agentToolsWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: agentToolsMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name: "launch_agent",
			Arguments: map[string]any{
				"agent": agentToolsAgent,
				"task":  agentToolsTask,
			},
		}},
		FinalAnswer: agentToolsFinalAnswer,
	})

	before := len(integrationInfra.ProviderRequests())

	sendSessionMessage(t, connection, workspace.sessionID, agentToolsMessage)
	readFramesUntilTurnCompleted(t, connection)

	requests := providerRequestsSince(before)
	require.Len(t, requests, 3)

	child := requests[1]
	assert.Contains(t, child[0].Content, variantTestAgentBody)

	childMessages := nonSystemMessages(t, child)
	require.Len(t, childMessages, 2)
	assert.Equal(t, "user", childMessages[0].Role)
	assert.Equal(t, agentToolsTask, childMessages[0].Content)
	assert.Equal(t, "user", childMessages[1].Role)
	assert.Contains(t, childMessages[1].Content, skillsTestPlanningMessagePrefix)

	tools := toolMessages(t, workspace.sessionID)
	require.Len(t, tools, 1)
	assert.False(t, toolFailed(tools[0]))

	output := struct {
		Name      string `json:"name"`
		Cancelled bool   `json:"cancelled"`
		Response  string `json:"response"`
	}{}
	require.NoError(t, json.Unmarshal([]byte(tools[0].Content), &output))
	assert.Equal(t, agentToolsAgent, output.Name)
	assert.False(t, output.Cancelled)
	assert.True(t, strings.HasPrefix(output.Response, agentToolsTextPrefix))
}

func toolMessages(t *testing.T, sessionID uuid.UUID) []api.Message {
	t.Helper()

	tools := make([]api.Message, 0)

	for _, message := range storedMessages(t, sessionID) {
		if message.Role == api.MessageRoleTool {
			tools = append(tools, message)
		}
	}

	return tools
}

func toolFailed(message api.Message) bool {
	return message.IsError != nil && *message.IsError
}

func decodeJobToolsResult(t *testing.T, message api.Message) jobToolsResult {
	t.Helper()

	result := jobToolsResult{}
	require.NoError(t, json.Unmarshal([]byte(message.Content), &result))

	return result
}

func findNotice(
	t *testing.T,
	sessionID uuid.UUID,
	noticeType string,
	found *api.SessionNotice,
) bool {
	t.Helper()

	response := apiRequest(
		t,
		http.MethodGet,
		sessionNoticesPath,
		nil,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	requireAPIStatus(t, response, http.StatusOK)

	for _, notice := range decodeResponse[api.SessionNoticePage](t, response).Notices {
		if notice.Type == noticeType {
			*found = notice

			return true
		}
	}

	return false
}
