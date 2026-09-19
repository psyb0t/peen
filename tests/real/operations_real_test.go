//go:build real

package realtest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	realOperationsReconfigurePath = realHarnessSessionPath + "/reconfigure"
	realOperationsDecisionsPath   = realHarnessSessionPath +
		"/profile-decisions"
	realOperationsCompactionsPath = realHarnessSessionPath + "/compactions"

	realOperationsPrimaryProfile   = "native"
	realOperationsSecondaryProfile = "native-alt"
	realOperationsUnknownProfile   = "does-not-exist"
	realOperationsReason           = "real suite profile move"

	realOperationsProfiles = `[{"name":"native","kind":"native"},` +
		`{"name":"native-alt","kind":"native"}]`

	// realOperationsCompactionContextTokens has to clear the system prompt plus
	// one full tool result, so the first large turn completes, while leaving
	// the second large turn over the line. Too small and the first turn fails
	// with nothing yet eligible to compact.
	realOperationsCompactionContextTokens = "20000"

	// realOperationsCompactionOutputTokens stays under the context budget
	// above. Peen refuses a configuration whose compaction output is not
	// smaller than its whole context, and the deployment .env sets a value
	// sized for the production budget.
	realOperationsCompactionOutputTokens = "800"

	realOperationsCompactionMode = "summarize"

	realOperationsFirstMessage = "Reply with a one sentence description of " +
		"what the file internal/status/status.go does. Read it first."
	realOperationsSecondMessage = "Now reply with one sentence about the " +
		"test file internal/status/status_test.go. Read it first."

	// The three compaction turns are shaped by two rules. Compaction keeps the
	// newest completed turn verbatim, so the turn it may cover has to be older
	// than that. And covering it has to free more than the summary allowance,
	// so that older turn is the one carrying a full tool result, not a bare
	// reply.
	realOperationsSmallMessage = "Reply with exactly OK. Make no tool calls."
	realOperationsLargeMessage = "Call read_file on NOTES.md, then reply with " +
		"one sentence about what it contains."

	realOperationsSignalTask = "Call run_command with exactly `sleep 300` " +
		"and report what happened. Make no other tool call."

	realOperationsSignalTimeout = 3 * time.Minute
)

// TestRealHarnessReconfiguresAnIdleSessionsProfile covers the execution profile
// move, which had unit coverage and no end-to-end test in any suite.
//
// The move is what an operator reaches for when a session needs a different
// environment, and it has to stop the session's current worker so the next turn
// starts a fresh generation. A decision recorded without that restart would
// attribute later turns to a profile that never ran them.
func TestRealHarnessReconfiguresAnIdleSessionsProfile(t *testing.T) {
	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	encodedUpstreams, err := json.Marshal(upstreams)
	require.NoError(t, err)

	fixture := newRealHarnessFixture(t)
	process := startRealHarnessPeenWith(
		t,
		buildRealHarnessBinary(t),
		fixture,
		string(encodedUpstreams),
		configured.DefaultModel,
		map[string]string{
			"PEEN_EXECUTION_PROFILES":        realOperationsProfiles,
			"PEEN_DEFAULT_EXECUTION_PROFILE": realOperationsPrimaryProfile,
		},
	)
	t.Cleanup(func() { process.stop(t) })

	sessionID := openRealHarnessSession(t, process, fixture.service)
	runRealHarnessTurn(t, process, sessionID, realOperationsFirstMessage)

	first := assertRealHarnessWorkerGeneration(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		fixture.service,
	)

	decision := reconfigureRealHarnessSession(
		t,
		process,
		sessionID,
		realOperationsSecondaryProfile,
		http.StatusOK,
	)
	require.NotNil(t, decision)
	assert.Equal(t, sessionID, decision.SessionId)
	assert.Equal(t, realOperationsSecondaryProfile, decision.ToProfile)
	assert.Equal(t, realOperationsReason, decision.Reason)
	require.NotNil(t, decision.FromProfile)
	assert.Equal(t, realOperationsPrimaryProfile, *decision.FromProfile)

	// An undefined profile is refused, and refusing it must not disturb the
	// profile the session already moved to.
	reconfigureRealHarnessSession(
		t,
		process,
		sessionID,
		realOperationsUnknownProfile,
		http.StatusForbidden,
	)

	runRealHarnessTurn(t, process, sessionID, realOperationsSecondMessage)

	generations := getRealHarnessJSON[api.WorkerGenerationPage](
		t,
		process.baseURL+realHarnessWorkersPath+"?limit=50",
		process.apiToken,
		sessionID,
	)
	require.Len(t, generations.Items, 2, "the move must start a new generation")

	// The page is newest first, so the profile change shows as the newer row
	// running under the new profile while the old one is stopped.
	assert.Equal(t, realOperationsSecondaryProfile, generations.Items[0].Profile)
	assert.Equal(t, realOperationsPrimaryProfile, generations.Items[1].Profile)
	assert.Equal(t, first, generations.Items[1].Id.String())
	assert.Equal(
		t,
		api.WorkerGenerationStateStopped,
		generations.Items[1].State,
	)

	decisions := getRealHarnessJSON[api.SessionProfileDecisionPage](
		t,
		process.baseURL+realOperationsDecisionsPath+"?limit=50",
		process.apiToken,
		sessionID,
	)
	require.NotEmpty(t, decisions.Items)
	assert.Equal(
		t,
		realOperationsSecondaryProfile,
		decisions.Items[0].ToProfile,
	)
}

// TestRealHarnessCompactsALongSession covers compaction end to end, which had
// unit coverage and no test that a real compaction model is ever called.
//
// Compaction is what keeps a long session inside its context budget. A
// compaction row that never appears means the session silently grows until the
// provider refuses it.
func TestRealHarnessCompactsALongSession(t *testing.T) {
	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	encodedUpstreams, err := json.Marshal(upstreams)
	require.NoError(t, err)

	fixture := newRealHarnessFixture(t)
	process := startRealHarnessPeenWith(
		t,
		buildRealHarnessBinary(t),
		fixture,
		string(encodedUpstreams),
		configured.DefaultModel,
		map[string]string{
			"PEEN_MAX_CONTEXT_TOKENS":           realOperationsCompactionContextTokens,
			"PEEN_COMPACTION_MAX_OUTPUT_TOKENS": realOperationsCompactionOutputTokens,
			// Summarize is the mode that calls a compaction model and records a
			// row. Under drop-oldest the provider library evicts whole units and
			// there is nothing durable to read back.
			"PEEN_COMPACTION_MODE": realOperationsCompactionMode,
		},
	)
	t.Cleanup(func() { process.stop(t) })

	sessionID := openRealHarnessSession(t, process, fixture.service)
	runRealHarnessTurn(t, process, sessionID, realOperationsLargeMessage)
	runRealHarnessTurn(t, process, sessionID, realOperationsSmallMessage)
	runRealHarnessTurn(t, process, sessionID, realOperationsLargeMessage)

	page := getRealHarnessJSON[api.CompactionPage](
		t,
		process.baseURL+realOperationsCompactionsPath+"?limit=50",
		process.apiToken,
		sessionID,
	)
	require.NotEmpty(
		t,
		page.Compactions,
		"a session over its context budget must compact; Peen output:\n%s",
		process.output.String(),
	)

	compaction := page.Compactions[0]
	assert.Equal(t, sessionID, compaction.SessionId)
	assert.NotEmpty(t, compaction.Summary)
	assert.NotEmpty(t, compaction.Model)
	assert.NotEmpty(t, compaction.PromptHash)
	assert.Positive(t, compaction.SourceMessageCount)
	assert.Positive(t, compaction.InputTokenCount)
	assert.Positive(t, compaction.SummaryTokenCount)
	assert.GreaterOrEqual(t, compaction.ToSequence, compaction.FromSequence)

	// The summary has to be reachable on its own, because that is how a client
	// rebuilding a compacted session reads what replaced the removed messages.
	single := getRealHarnessJSON[api.Compaction](
		t,
		process.baseURL+realOperationsCompactionsPath+"/"+compaction.Id.String(),
		process.apiToken,
		sessionID,
	)
	assert.Equal(t, compaction.Id, single.Id)
	assert.Equal(t, compaction.Summary, single.Summary)

	// A compacted model run is recorded under its own stage, so cost reporting
	// can separate compaction spend from turn spend.
	runs := getRealHarnessJSON[api.ModelRunPage](
		t,
		process.baseURL+realHarnessModelRunsPath+"?limit=200&stage=compaction",
		process.apiToken,
		sessionID,
	)
	require.NotEmpty(t, runs.ModelRuns)
	assert.Equal(
		t,
		api.ModelRunStageCompaction,
		runs.ModelRuns[0].Stage,
	)
}

// TestRealHarnessSignalsARunningJob covers the job signal endpoints, which had
// unit coverage and no end-to-end test.
//
// Signalling is the only way to stop a command an agent started that will not
// finish on its own, so it has to reach the real process group rather than only
// record the request.
func TestRealHarnessSignalsARunningJob(t *testing.T) {
	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	encodedUpstreams, err := json.Marshal(upstreams)
	require.NoError(t, err)

	fixture := newRealHarnessFixture(t)
	process := startRealHarnessPeen(
		t,
		buildRealHarnessBinary(t),
		fixture,
		string(encodedUpstreams),
		configured.DefaultModel,
	)
	t.Cleanup(func() { process.stop(t) })

	sessionID := openRealHarnessSession(t, process, fixture.service)
	connection := dialRealHarnessWebSocket(t, process)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	sent := writeRealHarnessMessage(
		t,
		connection,
		sessionID,
		realOperationsSignalTask,
	)

	job := awaitRealHarnessRunningJob(t, process, sessionID)
	assert.Equal(t, realHarnessLongCommand, job.Command)

	signalled := signalRealHarnessJob(t, process, sessionID, job.JobId)
	assert.True(t, signalled.Signalled)
	assert.Equal(t, job.JobId, signalled.JobId)

	// The turn ends once the killed command returns to the agent.
	require.Equal(t, sessionID, awaitRealHarnessWebSocketCompletion(
		t,
		process,
		connection,
		sent,
	))

	records := getRealHarnessJSON[api.JobSignalRecordPage](
		t,
		process.baseURL+realHarnessJobsPath+"/"+job.JobId.String()+"/signals",
		process.apiToken,
		sessionID,
	)
	require.NotEmpty(t, records.SignalRequests)

	record := records.SignalRequests[0]
	assert.Equal(t, job.JobId, record.JobId)
	assert.Equal(t, sessionID, record.SessionId)
	assert.Equal(t, api.JobSignalRecordSignalStop, record.Signal)
	assert.True(t, record.Accepted)
	assert.Equal(
		t,
		api.JobSignalRecordStateAtRequestRunning,
		record.StateAtRequest,
	)

	// A signalled job must not be recorded as a clean exit, or an operator
	// reading the history cannot tell a stopped command from a finished one.
	assert.NotEqual(t, int64(0), records.Job.ExitCode)
}

// runRealHarnessTurn sends one message and waits for the turn it starts.
func runRealHarnessTurn(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
	message string,
) {
	t.Helper()

	connection := dialRealHarnessWebSocket(t, process)
	defer func() { require.NoError(t, connection.Close()) }()

	sent := writeRealHarnessMessage(t, connection, sessionID, message)
	require.Equal(t, sessionID, awaitRealHarnessWebSocketCompletion(
		t,
		process,
		connection,
		sent,
	))
}

// reconfigureRealHarnessSession moves a session to a profile and checks the
// status. It returns the decision only for an accepted move.
func reconfigureRealHarnessSession(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
	profile string,
	wantStatus int,
) *api.SessionProfileDecision {
	t.Helper()

	payload, err := json.Marshal(api.ReconfigureSessionRequest{
		Profile: profile,
		Reason:  realOperationsReason,
	})
	require.NoError(t, err)

	request, err := http.NewRequest(
		http.MethodPost,
		process.baseURL+realOperationsReconfigurePath,
		bytes.NewReader(payload),
	)
	require.NoError(t, err)
	request.Header.Set(realHarnessContentType, realHarnessJSONMediaType)
	setRealHarnessSessionHeaders(request, process.apiToken, sessionID)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equalf(
		t,
		wantStatus,
		response.StatusCode,
		"Peen output:\n%s",
		process.output.String(),
	)

	if wantStatus != http.StatusOK {
		return nil
	}

	decision := api.SessionProfileDecision{}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&decision))

	return &decision
}

// awaitRealHarnessRunningJob waits for the agent to actually start the command,
// because a signal sent before the job exists has nothing to reach.
func awaitRealHarnessRunningJob(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
) api.Job {
	t.Helper()

	deadline := time.Now().Add(realOperationsSignalTimeout)
	for time.Now().Before(deadline) {
		page := getRealHarnessJSON[api.JobPage](
			t,
			process.baseURL+realHarnessJobsPath+"?limit=50",
			process.apiToken,
			sessionID,
		)

		for _, job := range page.Jobs {
			if job.State == api.JobStateRunning {
				return job
			}
		}

		time.Sleep(realHarnessPollInterval)
	}

	require.Failf(
		t,
		"no job started",
		"Peen output:\n%s",
		process.output.String(),
	)

	return api.Job{}
}

// signalRealHarnessJob asks the control plane to stop one running job.
func signalRealHarnessJob(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
	jobID uuid.UUID,
) api.JobSignalResponse {
	t.Helper()

	payload, err := json.Marshal(api.JobSignalRequest{
		Signal: api.JobSignalRequestSignalStop,
	})
	require.NoError(t, err)

	request, err := http.NewRequest(
		http.MethodPost,
		process.baseURL+realHarnessJobsPath+"/"+jobID.String()+"/signal",
		bytes.NewReader(payload),
	)
	require.NoError(t, err)
	request.Header.Set(realHarnessContentType, realHarnessJSONMediaType)
	setRealHarnessSessionHeaders(request, process.apiToken, sessionID)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equalf(
		t,
		http.StatusAccepted,
		response.StatusCode,
		"Peen output:\n%s",
		process.output.String(),
	)

	result := api.JobSignalResponse{}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))

	return result
}
