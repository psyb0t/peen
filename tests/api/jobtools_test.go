//go:build integration

package api_test

import (
	"encoding/json"
	"io"
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
	jobToolsCommand         = `read -r signal < "$RELEASE_PIPE"; echo '` + jobToolsOutputLine + `'`
	jobToolsReleaseCommand  = `printf 'continue\n' > "$RELEASE_PIPE"`
	jobToolsPurpose         = "api job tool fixture"
	jobToolsShortWait       = "300ms"
	jobToolsLongWait        = "1m"
	jobToolsBadTimeout      = "soon"
	jobToolsBadTimeoutError = `timeout "soon" must be a Go duration such ` +
		`as 90s, 5m, or 1h30m: time: invalid duration "soon": validation failed`
	jobToolsOldArgumentError = `decode tool arguments: json: unknown field ` +
		`"timeoutSeconds"`
	jobToolsStateRunning = "running"
	jobToolsStateExited  = "exited"

	jobNoticeWorkspacePrefix = "jobnotice-"
	jobNoticeMessage         = "start a short background job"
	jobNoticeFollowUp        = "what finished while you were away"
	jobNoticeFinalAnswer     = "the short job started"
	jobNoticeCommand         = "sleep 1"
	jobNoticePurpose         = "api job notice fixture"
	jobNoticeWait            = 30 * time.Second
	jobNoticePoll            = 200 * time.Millisecond

	backgroundJobUserMessage    = "run the scripted background job sequence"
	backgroundJobFollowUp       = "what finished while you were away"
	backgroundJobFixtureFile    = "background-job-fixture.txt"
	backgroundJobFixtureContent = "background job fixture\n"
	backgroundJobFixtureEdited  = "background job fixture edited\n"
	backgroundJobCommand        = "sleep 2 && echo background job done"
	backgroundJobPurpose        = "outlive the one second wait"
	backgroundJobWaitTimeout    = "1s"
	backgroundJobFinalAnswer    = "background job started"
	backgroundJobEventWait      = 15 * time.Second
	backgroundJobEventPoll      = 200 * time.Millisecond
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

func TestAPIStopKillsForegroundCommandAndItsChild(t *testing.T) {
	const task = "stop this foreground process tree"
	const command = `sleep 3600 & child=$!; printf '%s %s' "$$" "$child" > "$PID_PATH"; wait`
	workspace := openFreshWorkspace(t, "foreground-stop-", nil)
	connection := openSessionConnection(t, workspace.sessionID)
	pidPath := "/tmp/foreground-" + uuid.NewString()
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: task, FinalAnswer: "must remain interrupted",
		Calls: []testinfra.ScriptedToolCall{{
			Name: "runCommand", Arguments: map[string]any{
				"command": command, "purpose": task, "timeout": "1h",
				"environment": map[string]string{"PID_PATH": pidPath},
			},
		}},
	})
	sendSessionMessage(t, connection, workspace.sessionID, task)
	awaitFixtureCommand(t, "test", "-s", pidPath)
	pids := strings.Fields(readContainerFile(t, pidPath))
	require.Len(t, pids, 2)
	for _, pid := range pids {
		require.Zero(t, fixtureCommandExitCode(t, "kill", "-0", pid))
	}
	before := len(integrationInfra.ProviderRequests())
	response := apiRequest(t, http.MethodPost, sessionCancelPath, nil,
		withHeader(authenticatedHeaders(), headerSessionID,
			workspace.sessionID.String()))
	requireAPIStatus(t, response, http.StatusAccepted)
	for _, pid := range pids {
		// A zombie has exited but can still answer kill -0 until reaped.
		awaitFixtureCommand(t, "/bin/bash", "-c", `
if ! test -r "/proc/$1/stat"; then exit 0; fi
read -r pid name state rest < "/proc/$1/stat"
case "$state" in Z|X) exit 0;; *) exit 1;; esac
`, "foreground-stop", pid)
	}
	require.Eventually(t, func() bool {
		return !getSession(t, workspace.sessionID).ActiveTurn
	}, requestTimeout, jobNoticePoll)
	assert.Len(t, integrationInfra.ProviderRequests(), before)
}

func fixtureCommandExitCode(t *testing.T, command ...string) int {
	t.Helper()
	code, reader, err := integrationInfra.App.Exec(t.Context(), command)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, reader)
	require.NoError(t, err)

	return code
}

// waitJob waits for a real job up to its timeout: a short timeout reports it
// still running, a long one reports how it exited with its output. A timeout
// that is not a Go duration, and the retired timeoutSeconds argument, are
// refused as failed tool calls and run nothing.
func TestAPIWaitJobHonorsItsTimeoutThroughTheWorker(t *testing.T) {
	workspace := openFreshWorkspace(t, jobToolsWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)
	releasePipe := testinfra.ContainerWorkingDirectory + "/job-wait-" + uuid.NewString()
	require.Zero(t, fixtureCommandExitCode(t, "mkfifo", releasePipe))
	environment := map[string]string{"RELEASE_PIPE": releasePipe}

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
				Name: "runCommand",
				Arguments: map[string]any{
					"command":     jobToolsCommand,
					"purpose":     jobToolsPurpose,
					"background":  true,
					"environment": environment,
				},
			},
			{
				Name: "waitJob",
				ArgumentsFrom: func(results []string) map[string]any {
					return map[string]any{
						"jobId":   jobID(results),
						"timeout": jobToolsShortWait,
					}
				},
			},
			{
				Name: "runCommand",
				Arguments: map[string]any{
					"command":     jobToolsReleaseCommand,
					"purpose":     jobToolsPurpose,
					"environment": environment,
				},
			},
			{
				Name: "waitJob",
				ArgumentsFrom: func(results []string) map[string]any {
					return map[string]any{
						"jobId":   jobID(results),
						"timeout": jobToolsLongWait,
					}
				},
			},
			{
				Name: "runCommand",
				Arguments: map[string]any{
					"command": "true",
					"purpose": jobToolsPurpose,
					"timeout": jobToolsBadTimeout,
				},
			},
			{
				Name: "runCommand",
				Arguments: map[string]any{
					"command":        "true",
					"purpose":        jobToolsPurpose,
					"timeoutSeconds": 5,
				},
			},
			{
				Name: "waitJob",
				ArgumentsFrom: func(results []string) map[string]any {
					return map[string]any{
						"jobId":   jobID(results),
						"timeout": jobToolsBadTimeout,
					}
				},
			},
			{
				Name: "waitJob",
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
	require.Len(t, tools, 8)

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

	require.False(t, toolFailed(tools[2]))
	finished := decodeJobToolsResult(t, tools[3])
	assert.True(t, finished.Found)
	assert.False(t, finished.Running)
	assert.Equal(t, jobToolsStateExited, finished.State)
	require.NotNil(t, finished.ExitCode)
	assert.Equal(t, 0, *finished.ExitCode)
	assert.Contains(t, finished.Stdout, jobToolsOutputLine)
	shortBound, err := time.ParseDuration(jobToolsShortWait)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, finished.DurationMs, shortBound.Milliseconds())

	parsed, err := time.ParseDuration(finished.Duration)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(finished.DurationMs)*time.Millisecond, parsed)

	assert.True(t, toolFailed(tools[4]))
	assert.Equal(t, jobToolsBadTimeoutError,
		toolOutputText(t, tools[4].Content))

	assert.True(t, toolFailed(tools[5]))
	assert.Equal(t, jobToolsOldArgumentError,
		toolOutputText(t, tools[5].Content))

	assert.True(t, toolFailed(tools[6]))
	assert.Equal(t, jobToolsBadTimeoutError,
		toolOutputText(t, tools[6].Content))

	assert.True(t, toolFailed(tools[7]))
	assert.Equal(t, jobToolsOldArgumentError,
		toolOutputText(t, tools[7].Content))
}

// A job that ends after its runCommand call returned publishes job.exited
// with its run time as durationMs and as a Go duration string, and the next
// turn receives that same data in its events message.
func TestAPIJobExitedNoticeCarriesTheDuration(t *testing.T) {
	workspace := openFreshWorkspace(t, jobNoticeWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: jobNoticeMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name: "runCommand",
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

// childAgentRequest returns the one request a child agent made, told apart by
// the core agent's instructions in its system prompt.
func childAgentRequest(
	t *testing.T,
	requests [][]testinfra.ProviderMessage,
) []testinfra.ProviderMessage {
	t.Helper()

	var child []testinfra.ProviderMessage

	for _, request := range requests {
		if !strings.Contains(request[0].Content, variantTestAgentBody) {
			continue
		}

		require.Nil(t, child, "the child made more than one request")

		child = request
	}

	require.NotNil(t, child, "the child never reached the provider")

	return child
}

func decodeJobToolsResult(t *testing.T, message api.Message) jobToolsResult {
	t.Helper()

	result := jobToolsResult{}
	require.NoError(t, json.Unmarshal(
		[]byte(toolOutputText(t, message.Content)), &result))

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

// A command still running when its runCommand call returns finishes as a
// background job. Its job.exited event goes through the real job registry and
// event bus, is stored as a session notice, and opens the next turn as an
// injected update.
func TestAPIBackgroundJobPublishesItsCompletion(t *testing.T) {
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)

	fixturePath := hostToolsContainerPath(backgroundJobFixtureFile)
	seedContainerFile(t, fixturePath, backgroundJobFixtureContent)

	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage:       backgroundJobUserMessage,
		ReadFileArguments: map[string]any{"path": fixturePath},
		EditFileArguments: map[string]any{
			"path": fixturePath,
			"edits": []map[string]any{{
				"old": backgroundJobFixtureContent,
				"new": backgroundJobFixtureEdited,
			}},
		},
		RunCommandArguments: map[string]any{
			"command": backgroundJobCommand,
			"purpose": backgroundJobPurpose,
			"timeout": backgroundJobWaitTimeout,
		},
		FinalAnswer: backgroundJobFinalAnswer,
	})

	result := sendAPIWebSocketMessage(t, backgroundJobUserMessage)
	integrationInfra.DisableScriptedToolTurn()

	require.Eventually(t, func() bool {
		return hasNoticeOfType(t, result.sessionID, hostToolsJobExitedType)
	}, backgroundJobEventWait, backgroundJobEventPoll,
		"the background job published job.exited")

	sendAPIWebSocketMessage(t, backgroundJobFollowUp)

	messages := collectAllMessages(t, result.sessionID)
	delivered := false
	for _, message := range messages {
		if message.Injected == nil || !*message.Injected {
			continue
		}
		if strings.Contains(message.Content, hostToolsJobEventMarker) &&
			strings.Contains(message.Content, hostToolsJobExitedType) {
			delivered = true
		}
	}
	assert.True(t, delivered, "the next turn opened with the job.exited update")
}

func hasNoticeOfType(t *testing.T, sessionID uuid.UUID, noticeType string) bool {
	t.Helper()

	response := apiRequest(
		t,
		http.MethodGet,
		sessionNoticesPath,
		nil,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	requireAPIStatus(t, response, http.StatusOK)

	page := decodeResponse[api.SessionNoticePage](t, response)
	for _, notice := range page.Notices {
		if notice.Type == noticeType {
			return true
		}
	}

	return false
}
