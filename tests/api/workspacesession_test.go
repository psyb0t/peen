//go:build integration

package api_test

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	sharedWorkspaceJob         = "sharedWorkspaceTick"
	sharedWorkspaceCronMessage = "Report the scheduled tick"
	sharedWorkspaceOpenAIText  = "Ask through the OpenAI API"
	sharedWorkspaceBrowserText = "Send the browser message "
	sharedWorkspaceBusyReason  = "workspace session is busy"
	sharedWorkspaceRunsPath    = apiBasePath + "/cron/runs?job=" +
		sharedWorkspaceJob + "&limit=200"
	sharedWorkspaceWait     = 30 * time.Second
	sharedWorkspacePoll     = 100 * time.Millisecond
	sharedWorkspaceAttempts = 10
	sharedWorkspaceCron     = `jobs:
  - name: ` + sharedWorkspaceJob + `
    schedule: "*/2 * * * * *"
    timeZone: UTC
    workspace: ` + variantTestWorkspace + `
    message: "` + sharedWorkspaceCronMessage + `"
    timeout: 15s
`
)

// The control surface, the OpenAI API and cron all reach one workspace
// through its one session and transcript. A scheduled tick that comes while
// a browser turn runs is skipped as busy instead of interleaving with it.
func TestAPIOneWorkspaceSessionAcrossEntryPoints(t *testing.T) {
	variant := startAPIVariant(t, testinfra.VariantOptions{
		Environment: map[string]string{
			"PEEN_CRON": "true", "PEEN_OPENAI_API": "true",
			"PEEN_OPENAI_WORKSPACE": variantTestWorkspace,
		},
		CronConfiguration: sharedWorkspaceCron,
	})
	sessionID := openVariantSession(t, variant)

	completed := awaitCronRun(t, variant, func(run api.CronRun) bool {
		return run.State == api.CronRunStateCompleted
	})
	require.NotNil(t, completed.SessionId)
	assert.Equal(t, sessionID, *completed.SessionId)

	assert.Equal(t, sessionID, openAIWhenIdle(t, variant))

	browserText := runBrowserTurnThroughBusyTick(t, variant, sessionID)

	sessions := decodeResponse[api.SessionPage](t, variantRequest(t, variant,
		http.MethodGet, apiBasePath+"/sessions?limit=200", nil))
	workspaceSessions := make([]uuid.UUID, 0, 1)
	for _, listed := range sessions.Items {
		if listed.Workspace == variantTestWorkspace {
			workspaceSessions = append(workspaceSessions, listed.Id)
		}
	}
	assert.Equal(t, []uuid.UUID{sessionID}, workspaceSessions)

	users := userMessageContents(variantMessages(t, variant, sessionID))
	assert.Equal(t, 1, countContaining(users, sharedWorkspaceOpenAIText), users)
	assert.Equal(t, 1, countContaining(users, browserText), users)
	assert.GreaterOrEqual(t,
		countContaining(users, sharedWorkspaceCronMessage), 1, users)
}

// openAIWhenIdle sends one OpenAI request by the configured workspace,
// retrying while a scheduled turn holds the session, and returns the session
// it answered from.
func openAIWhenIdle(t *testing.T, variant *testinfra.Infra) uuid.UUID {
	t.Helper()
	deadline := time.Now().Add(sharedWorkspaceWait)
	for time.Now().Before(deadline) {
		response, sessionID := openAIRequest(t, variant,
			`{"messages":[{"role":"user","content":"`+
				sharedWorkspaceOpenAIText+`"}]}`, "")
		if response.StatusCode == http.StatusConflict {
			time.Sleep(sharedWorkspacePoll)

			continue
		}
		require.Equal(t, http.StatusOK, response.StatusCode,
			string(response.Body))
		parsed, err := uuid.Parse(sessionID)
		require.NoError(t, err)

		return parsed
	}
	t.Fatal("the workspace session stayed busy for every OpenAI attempt")

	return uuid.Nil
}

// runBrowserTurnThroughBusyTick holds a browser turn's model call open until
// a scheduled tick is skipped as busy, then lets the turn finish. A cron turn
// may take the held call instead, so it retries with a new message until the
// held turn is the browser's. It returns the browser message that ran.
func runBrowserTurnThroughBusyTick(
	t *testing.T,
	variant *testinfra.Infra,
	sessionID uuid.UUID,
) string {
	t.Helper()
	for attempt := range sharedWorkspaceAttempts {
		before := busySkipIDs(t, variant)
		hold, err := variant.HoldNextCompletion()
		require.NoError(t, err)
		connection := dialVariantSession(t, variant, sessionID)
		text := sharedWorkspaceBrowserText + strconv.Itoa(attempt)
		sendSessionMessage(t, connection, sessionID, text)
		select {
		case <-hold.Observed:
		case <-time.After(sharedWorkspaceWait):
			hold.Release()
			t.Fatal("no model call reached the provider")
		}
		if !browserTurnRunning(t, variant, sessionID) {
			hold.Release()

			continue
		}
		skipped := awaitCronRun(t, variant, func(run api.CronRun) bool {
			return run.State == api.CronRunStateSkipped &&
				run.Error != nil && *run.Error == sharedWorkspaceBusyReason &&
				!slices.Contains(before, run.Id)
		})
		hold.Release()
		require.NotNil(t, skipped.SessionId)
		assert.Equal(t, sessionID, *skipped.SessionId)
		assert.NotNil(t, skipped.StartedAt)
		assert.NotNil(t, skipped.FinishedAt)
		readFramesUntilTurnCompleted(t, connection)

		return text
	}
	t.Fatal("a scheduled turn took every held model call")

	return ""
}

// browserTurnRunning reports whether the session's running turn was started
// by a client message rather than by a scheduled run, whose turn reuses the
// run's ID as its request ID.
func browserTurnRunning(
	t *testing.T,
	variant *testinfra.Infra,
	sessionID uuid.UUID,
) bool {
	t.Helper()
	response := variantSessionRequest(t, variant, http.MethodGet,
		sessionPath+"/turns?limit=1", sessionID)
	requireAPIStatus(t, response, http.StatusOK)
	turns := decodeResponse[api.TurnPage](t, response)
	require.Len(t, turns.Turns, 1)
	if turns.Turns[0].State != api.TurnStateRunning {
		return false
	}

	return !slices.ContainsFunc(cronRuns(t, variant), func(run api.CronRun) bool {
		return run.Id == turns.Turns[0].RequestId
	})
}

func cronRuns(t *testing.T, variant *testinfra.Infra) []api.CronRun {
	t.Helper()
	response := variantRequest(t, variant, http.MethodGet,
		sharedWorkspaceRunsPath, nil)
	requireAPIStatus(t, response, http.StatusOK)

	return decodeResponse[api.CronRunPage](t, response).Items
}

func busySkipIDs(t *testing.T, variant *testinfra.Infra) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0)
	for _, run := range cronRuns(t, variant) {
		if run.State == api.CronRunStateSkipped && run.Error != nil &&
			*run.Error == sharedWorkspaceBusyReason {
			ids = append(ids, run.Id)
		}
	}

	return ids
}

// awaitCronRun polls the job's history until a run matches.
func awaitCronRun(
	t *testing.T,
	variant *testinfra.Infra,
	matches func(api.CronRun) bool,
) api.CronRun {
	t.Helper()
	deadline := time.Now().Add(sharedWorkspaceWait)
	runs := []api.CronRun{}
	for time.Now().Before(deadline) {
		runs = cronRuns(t, variant)
		if index := slices.IndexFunc(runs, matches); index >= 0 {
			return runs[index]
		}
		time.Sleep(sharedWorkspacePoll)
	}
	seen := make([]string, 0, len(runs))
	for _, run := range runs {
		reason := ""
		if run.Error != nil {
			reason = *run.Error
		}
		seen = append(seen, string(run.State)+" "+reason)
	}
	logs, err := variant.AppLogs(t.Context())
	require.NoError(t, err)
	scheduled := make([]string, 0)
	for line := range strings.SplitSeq(logs, "\n") {
		if strings.Contains(line, "schedul") || strings.Contains(line, "cron") {
			scheduled = append(scheduled, line)
		}
	}
	t.Fatalf("no scheduled run matched in time; runs: %v; log: %s", seen,
		strings.Join(scheduled, "\n"))

	return api.CronRun{}
}

func countContaining(contents []string, needle string) int {
	count := 0
	for _, content := range contents {
		count += strings.Count(content, needle)
	}

	return count
}
