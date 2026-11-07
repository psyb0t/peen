//go:build real

package realtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertRealHarnessFiles(t *testing.T, fixture realHarnessFixture) {
	t.Helper()

	implementation, err := os.ReadFile(fixture.implementation)
	require.NoError(t, err)
	assert.Contains(t, string(implementation), "return \"ready\"")

	// The assertion is the comparison the test makes, not the wording of its
	// failure message. A model that updates the comparison and leaves the
	// message reading "want pending" has still made the change the task asked
	// for, and the fixture test passes either way.
	testContent, err := os.ReadFile(fixture.testFile)
	require.NoError(t, err)
	assert.Contains(t, string(testContent), "!= \"ready\"")
	assert.NotContains(t, string(testContent), "!= \"pending\"")
}

func assertRealHarnessTranscript(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) {
	t.Helper()

	messages := listRealHarnessMessages(t, baseURL, apiToken, sessionID)
	assertRealHarnessTranscriptMessages(t, messages)
}

func assertRealHarnessTranscriptMessages(t *testing.T, messages []api.Message) {
	t.Helper()

	names := realHarnessToolCallNames(messages)
	assertRealHarnessToolSubsequence(
		t,
		names,
		[]string{"useSkill", "launchAgent", "readFile", "applyPatch", "runCommand"},
	)
	assertRealHarnessDirectFileToolsWereDenied(t, messages)
}

func assertRealHarnessDirectFileToolsWereDenied(t *testing.T, messages []api.Message) {
	t.Helper()

	toolNames := make(map[string]string)
	for _, message := range messages {
		if message.ToolCalls == nil {
			continue
		}

		for _, toolCall := range *message.ToolCalls {
			toolNames[toolCall.Id] = toolCall.Name
		}
	}

	for _, message := range messages {
		if message.ToolCallId == nil {
			continue
		}

		toolName := toolNames[*message.ToolCallId]
		if toolName != "writeFile" && toolName != "editFile" {
			continue
		}

		require.NotNil(t, message.IsError)
		assert.True(t, *message.IsError)
		assert.Contains(t, message.Content, realHarnessPatchOnlyReason)
	}
}

func realHarnessToolCallNames(messages []api.Message) []string {
	names := make([]string, 0)
	for _, message := range messages {
		if message.ToolCalls == nil {
			continue
		}

		for _, toolCall := range *message.ToolCalls {
			names = append(names, toolCall.Name)
		}
	}

	return names
}

func assertRealHarnessToolSubsequence(t *testing.T, names, wanted []string) {
	t.Helper()

	position := 0
	for _, name := range names {
		if position < len(wanted) && name == wanted[position] {
			position++
		}
	}

	assert.Equal(t, len(wanted), position, "tool sequence: %v", names)
}

func assertRealHarnessAgentRun(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) {
	t.Helper()

	page := getRealHarnessJSON[api.AgentRunPage](
		t,
		baseURL+realHarnessAgentRunsPath+"?limit=50",
		apiToken,
		sessionID,
	)
	require.NotEmpty(t, page.Agents)
	assert.Equal(t, realHarnessReviewer, page.Agents[0].Name)
	assert.Equal(t, "stored", string(page.Agents[0].Definition))
	assert.Equal(t, "completed", string(page.Agents[0].State))
}

func assertRealHarnessJob(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	service string,
) {
	t.Helper()

	page := getRealHarnessJSON[api.JobPage](
		t,
		baseURL+realHarnessJobsPath+"?limit=50",
		apiToken,
		sessionID,
	)
	require.NotEmpty(t, page.Jobs)

	found := false
	for _, job := range page.Jobs {
		assert.Equal(t, realHarnessFixtureTestCommand, job.Command)
		assert.Equal(t, service, job.Directory)
		assert.Equal(t, int64(0), job.ExitCode)
		found = true
	}

	assert.True(t, found)
}

// assertRealHarnessWorkerGeneration checks the durable record of the process
// that actually ran the turn, and returns its identifier.
//
// The control agent space keeps the worker in a child process of the
// controller, so the generation must carry neither a container nor an image
// digest. Those belong to a Docker generation and their presence here would
// mean the controller launched something other than what the agent space asked
// for.
func assertRealHarnessWorkerGeneration(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	workspace string,
) string {
	t.Helper()

	page := getRealHarnessJSON[api.WorkerGenerationPage](
		t,
		baseURL+realHarnessWorkersPath+"?limit=50",
		apiToken,
		sessionID,
	)
	require.Len(t, page.Items, 1)

	generation := page.Items[0]
	assert.Equal(t, sessionID, generation.SessionId)
	assert.Equal(t, workspace, generation.Workspace)
	assert.Equal(t, api.WorkerGenerationKindControl, generation.Kind)
	assert.Equal(t, api.WorkerGenerationStateReady, generation.State)
	assert.Nil(t, generation.ContainerId)
	assert.Nil(t, generation.ImageDigest)
	assert.Nil(t, generation.FailureDetail)
	assert.Nil(t, generation.EndedAt)
	require.NotNil(t, generation.StartedAt)

	return generation.Id.String()
}

// assertRealHarnessDurableTurns checks the turn rows the session produced and
// returns their identifiers, so callers can check that every other durable
// record points back at a turn that really exists.
func assertRealHarnessDurableTurns(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	workspace string,
	wantCount int,
) map[uuid.UUID]struct{} {
	t.Helper()

	page := getRealHarnessJSON[api.TurnPage](
		t,
		baseURL+realHarnessTurnsPath+"?limit=200",
		apiToken,
		sessionID,
	)
	require.Len(t, page.Turns, wantCount)

	turnIDs := make(map[uuid.UUID]struct{}, len(page.Turns))
	for _, turn := range page.Turns {
		assert.Equal(t, sessionID, turn.SessionId)
		assert.Equal(t, workspace, turn.Workspace)
		assert.Equal(t, api.TurnStateCompleted, turn.State)
		assert.False(t, turn.CancelRequested)
		assert.Empty(t, turn.FailureClassification)
		assert.NotEqual(t, uuid.Nil, turn.RequestId)
		assert.NotNil(t, turn.CompletedAt)
		turnIDs[turn.Id] = struct{}{}
	}

	return turnIDs
}

// assertRealHarnessTurnSnapshots follows a turn's context and prompt hashes to
// the snapshots they name.
//
// The hashes are how a later reader reconstructs what the model was actually
// given. A turn that stored a hash no snapshot resolves would read as complete
// and still be impossible to audit.
func assertRealHarnessTurnSnapshots(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	service string,
) {
	t.Helper()

	page := getRealHarnessJSON[api.TurnPage](
		t,
		baseURL+realHarnessTurnsPath+"?limit=200",
		apiToken,
		sessionID,
	)
	require.NotEmpty(t, page.Turns)

	turn := page.Turns[0]
	require.NotNil(t, turn.ContextSnapshotHash)
	require.NotNil(t, turn.PromptSnapshotHash)

	context := getRealHarnessJSON[api.ContextSnapshot](
		t,
		baseURL+realHarnessContextSnapshotsPath+"/"+*turn.ContextSnapshotHash,
		apiToken,
		sessionID,
	)
	assert.Equal(t, *turn.ContextSnapshotHash, context.Hash)
	assert.NotEmpty(t, context.Manifest)
	assert.Contains(t, context.ResolvedContent, realHarnessServiceRuleMarker)

	prompt := getRealHarnessJSON[api.PromptSnapshot](
		t,
		baseURL+realHarnessPromptSnapshotsPath+"/"+*turn.PromptSnapshotHash,
		apiToken,
		sessionID,
	)
	assert.Equal(t, *turn.PromptSnapshotHash, prompt.Hash)
	assert.NotEmpty(t, prompt.EffectivePrompt)

	// The workspace the turn ran in must be the one the fixture opened, and the
	// prompt is where a wrong workspace would first show.
	assert.Contains(t, prompt.EffectivePrompt+context.ResolvedContent, service)
}

// assertRealHarnessDurableEvents walks the whole durable event stream and
// checks the identity every event must carry.
//
// The sequence check matters most: a client rebuilding a session from another
// machine replays these in order, so a gap or a repeat is a corrupt transcript
// rather than a cosmetic flaw.
func assertRealHarnessDurableEvents(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	turnIDs map[uuid.UUID]struct{},
	generationID string,
) []api.TranscriptEvent {
	t.Helper()

	events := listRealHarnessEvents(t, baseURL, apiToken, sessionID)
	require.NotEmpty(t, events)

	previousSequence := int64(0)
	for index, event := range events {
		assert.Equalf(t, sessionID, event.SessionId, "event %d", index)
		assert.NotEqual(t, uuid.Nil, event.Id)
		assert.Greaterf(
			t,
			event.Sequence,
			previousSequence,
			"event %d type %q sequence must increase",
			index,
			event.Type,
		)
		previousSequence = event.Sequence

		if event.TurnId != uuid.Nil {
			assert.Containsf(
				t,
				turnIDs,
				event.TurnId,
				"event %d type %q names an unknown turn",
				index,
				event.Type,
			)
		}

		if event.WorkerGenerationId != nil {
			assert.Equalf(
				t,
				generationID,
				*event.WorkerGenerationId,
				"event %d type %q came from an unexpected generation",
				index,
				event.Type,
			)
		}
	}

	return events
}

// assertRealHarnessModelRuns checks that the provider calls the turn made were
// recorded with the model, usage, and per-round accounting the session needs to
// report cost.
func assertRealHarnessModelRuns(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	turnIDs map[uuid.UUID]struct{},
	modelID string,
) {
	t.Helper()

	page := getRealHarnessJSON[api.ModelRunPage](
		t,
		baseURL+realHarnessModelRunsPath+"?limit=200&stage=turn",
		apiToken,
		sessionID,
	)
	require.NotEmpty(t, page.ModelRuns)

	for _, run := range page.ModelRuns {
		assert.Equal(t, sessionID, run.SessionId)
		assert.Equal(t, api.ModelRunStageTurn, run.Stage)
		assert.Equal(t, api.ModelRunStateCompleted, run.State)
		assert.Contains(t, turnIDs, run.TurnId)
		assert.Equal(t, modelID, run.RequestedModelId)
		assert.NotEmpty(t, run.ConnectionName)
		assert.Empty(t, run.FailureClassification)
		assert.NotEmpty(t, run.ResponseUsage)
		assert.NotEmpty(t, run.ResponseMessages)
	}

	assertRealHarnessModelCalls(t, baseURL, apiToken, sessionID, page.ModelRuns[0])
}

// assertRealHarnessModelCalls checks the per-round provider records behind one
// model run: rounds numbered from one with no gap, and real token accounting.
func assertRealHarnessModelCalls(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	run api.ModelRun,
) {
	t.Helper()

	page := getRealHarnessJSON[api.ModelCallPage](
		t,
		fmt.Sprintf(
			"%s%s/%s/calls?limit=200",
			baseURL,
			realHarnessModelRunsPath,
			run.Id.String(),
		),
		apiToken,
		sessionID,
	)
	require.NotEmpty(t, page.Calls)

	completionTokens := int64(0)

	for index, call := range page.Calls {
		assert.Equal(t, sessionID, call.SessionId)
		assert.Equal(t, run.Id, call.ModelRunId)
		// Rounds are zero-based and the page is ordered by round, so the index
		// is the round a complete run must hold at that position.
		assert.Equalf(t, int64(index), call.Round, "round at position %d", index)
		assert.Equal(t, api.ModelCallStateCompleted, call.State)
		assert.Positivef(t, call.PromptTokens, "round %d prompt tokens", call.Round)
		assert.Positivef(t, call.TotalTokens, "round %d total tokens", call.Round)
		assert.NotEmptyf(t, call.RequestMessages, "round %d request messages", call.Round)
		assert.NotEmptyf(t, call.RequestTools, "round %d request tools", call.Round)
		assert.NotNilf(t, call.ResponseMessage, "round %d response message", call.Round)

		completionTokens += call.CompletionTokens
	}

	// Summed rather than per round, because a single round may legitimately
	// report no completion tokens while a run that produced none did not
	// record usage at all.
	assert.Positive(t, completionTokens)
}

// assertRealHarnessInjectedEventDelivered checks that a hook-emitted event
// reached the running turn's transcript.
//
// This is event injection, not user-message queueing. The two produce similar
// looking transcript entries, and the queueing contract is checked against a
// live turn in the session suite instead.
func assertRealHarnessInjectedEventDelivered(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) {
	t.Helper()

	messages := listRealHarnessMessages(t, baseURL, apiToken, sessionID)
	for _, message := range messages {
		if strings.Contains(message.Content, realHarnessPatchEvent) {
			return
		}
	}

	require.Failf(t, "patch event was not delivered", "event type %q", realHarnessPatchEvent)
}

func assertRealHarnessAuditLog(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	var content strings.Builder
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}

		fileContent, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		require.NoError(t, readErr)
		content.Write(fileContent)
	}

	for _, message := range []string{
		"hook lifecycle event started",
		"hook group started",
		"hook action started",
		"hook command started",
		"skill activated",
		"child agent started",
		"child agent completed",
		"hook session event started",
		"event wake turn started",
		"config-remember-read",
		"record-read-state",
		"config-require-read",
		"allow-only-after-read",
		"config-allow-only-fixture-test",
		"deny-unapproved-command",
		"workspace-patch-policy",
		"workspace-patch-policy-action",
		"catalog-patch-policy",
		"catalog-patch-policy-action",
		"service-patch-event",
		"emit-patch-event",
	} {
		assert.Contains(t, content.String(), message)
	}
}
