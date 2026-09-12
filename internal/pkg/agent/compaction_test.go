package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// compactionTestTokensPerMessage is the flat per-message charge the test
	// token counter applies. Message count times this constant is the
	// transcript's estimated cost throughout the "standard" scenario tests.
	compactionTestTokensPerMessage = 100

	// compactionTestMaxContextTokens and compactionTestOutputTokens are the
	// budget shared by every test built on the "three ordinary turns then a
	// triggering turn" scenario.
	compactionTestMaxContextTokens = 600
	compactionTestOutputTokens     = 50

	// compactionOrdinaryTurnMessages is how many durable rows one ordinary,
	// tool-free turn contributes: one user message, one assistant reply.
	compactionOrdinaryTurnMessages = 2

	// compactionTurnsBeforeFirstTrigger ordinary turns run before the turn
	// that first exceeds compactionTestMaxContextTokens.
	compactionTurnsBeforeFirstTrigger = 3
	// compactionTurnsBeforeSecondTrigger is the total run count that reaches
	// the second, superseding compaction.
	compactionTurnsBeforeSecondTrigger = 5

	// compactionFirstToIndex is the ascending message index of the last row
	// the first compaction covers: turn two's assistant reply.
	compactionFirstToIndex = 3
	// compactionSecondToIndex is the ascending message index of the last row
	// the second, superseding compaction covers: turn three's assistant
	// reply.
	compactionSecondToIndex = 5

	compactionFirstSourceMessageCount  = 4
	compactionSecondSourceMessageCount = 6

	compactionUserTextFormat      = "user turn %d"
	compactionAssistantTextFormat = "assistant turn %d"

	compactionScenarioSummaryText   = "condensed prior turns"
	compactionWhitespaceSummaryText = "   \n\t  "
	compactionCallModelSummaryText  = "model produced summary text"
	compactionStubModelID           = "stub-summarizer-model"
	compactionStubInputTokens       = 400
	compactionStubOutputTokens      = 40

	// compactionUnavailableMaxContextTokens is tuned so the hook fires on the
	// second run while only one, already-newest, turn is on record: nothing
	// is eligible to compact.
	compactionUnavailableMaxContextTokens = 300
	compactionUnavailableOutputTokens     = 50

	compactionAbsentFileName = "absent.txt"

	// compactionToolBoundaryMaxContextTokens and
	// compactionToolBoundaryOutputTokens are tuned so the fifth run's hook
	// selects exactly the leading user message plus the whole assistant
	// tool-call unit, never splitting it.
	compactionToolBoundaryMaxContextTokens = 1100
	compactionToolBoundaryOutputTokens     = 50
	compactionToolBoundaryCallID           = "compaction_tool_call"
	compactionToolBoundaryFinalText        = "tool turn finished"
	compactionToolBoundaryMessageCount     = 12
	compactionToolBoundaryAssistantIndex   = 1
	compactionToolBoundaryResultIndex      = 2

	compactionFailedTurnUserText       = "attempted risky turn"
	compactionFailedToolCallID         = "compaction_failed_tool_call"
	compactionFailedTurnUserIndex      = 2
	compactionFailedTurnAssistantIndex = 3
	compactionFailedTurnToolIndex      = 4
	compactionFailedToIndex            = 6
	compactionFailedMessageCount       = 11

	// compactionInsufficientMessageBytes, ...BudgetMargin, and
	// ...SummaryBytes tune a byte-weighted counter (see compactionTokenCounter)
	// instead of the standard per-message one: a per-message counter can never
	// reproduce ErrCompactionInsufficient, because selectPrefix's placeholder
	// candidate and commit's real text cost the exact same message COUNT. The
	// stub's summary must be large enough, in BYTES, to blow the budget only
	// once the real text replaces the short placeholder.
	compactionInsufficientMessageBytes = 200
	compactionInsufficientBudgetMargin = 1000
	compactionInsufficientOutputTokens = 50
	compactionInsufficientSummaryBytes = 10000
	compactionInsufficientMessageChar  = "m"
	compactionInsufficientSummaryChar  = "g"

	compactionSessionIsolationRunsA = 4
	compactionSessionIsolationRunsB = 2
)

var (
	errCompactionSummarizeFailed = errors.New(
		"compaction test summarizer failed",
	)
	errCompactionHookFailed = errors.New("compaction test hook failed")
	errCompactionTurnFailed = errors.New("compaction test turn failed")
)

// compactionTokenCounter is a deterministic elelem.TokenCounter driving the
// budget arithmetic this file's tests assert against. Cost scales with
// message count, with an optional per-byte surcharge for the one test where a
// real summary's length has to matter.
type compactionTokenCounter struct {
	perMessage int
	perByte    int
}

func (c compactionTokenCounter) Count(
	messages []elelem.Message,
	_ []elelem.Tool,
) (int, error) {
	total := len(messages) * c.perMessage

	for _, message := range messages {
		total += len(message.Text()) * c.perByte
	}

	return total, nil
}

var _ elelem.TokenCounter = compactionTokenCounter{}

// compactionSummarizeStub is a summarizeFunc test double. It records every
// transcript it was asked to condense and returns one configured outcome,
// so a test can prove what the compactor sent without a second provider call.
type compactionSummarizeStub struct {
	text        string
	err         error
	calls       int
	transcripts []string
}

func newCompactionSummarizeStub(text string) *compactionSummarizeStub {
	return &compactionSummarizeStub{text: text}
}

func (s *compactionSummarizeStub) summarize(
	_ context.Context,
	transcript string,
) (compactionSummary, error) {
	s.calls++
	s.transcripts = append(s.transcripts, transcript)

	if s.err != nil {
		return compactionSummary{}, s.err
	}

	return compactionSummary{
		Text:         s.text,
		ModelID:      compactionStubModelID,
		InputTokens:  compactionStubInputTokens,
		OutputTokens: compactionStubOutputTokens,
	}, nil
}

func (s *compactionSummarizeStub) lastTranscript() string {
	if len(s.transcripts) == 0 {
		return ""
	}

	return s.transcripts[len(s.transcripts)-1]
}

// compactionScenario bundles one standard scenario run: three ordinary turns
// (or five, for the superseding tests) against a scripted driver and a
// recording summarizer stub.
type compactionScenario struct {
	fixture   runtimeFixture
	driver    *elelemtest.ScriptedDriver
	sessionID uuid.UUID
	results   []*TurnResult
	stub      *compactionSummarizeStub
}

// setupCompactionScenario drives runs ordinary turns through a standard
// compaction-mode fixture, recording every transcript the stub summarizer
// received.
func setupCompactionScenario(t *testing.T, runs int) compactionScenario {
	t.Helper()

	driver := elelemtest.NewScriptedDriver(compactionScriptedTurns(runs)...).
		WithTokenCounter(compactionTokenCounter{
			perMessage: compactionTestTokensPerMessage,
		})
	fixture := newStandardCompactionFixture(t, driver)

	stub := newCompactionSummarizeStub(compactionScenarioSummaryText)
	fixture.runtime.compactionOptions.Summarize = stub.summarize

	sessionID, results := runCompactionTurns(t, fixture, nil, 1, runs)

	return compactionScenario{
		fixture:   fixture,
		driver:    driver,
		sessionID: sessionID,
		results:   results,
		stub:      stub,
	}
}

// newStandardCompactionFixture builds a runtime fixture in summarize mode
// under the shared test budget, leaving compactionOptions.Summarize at its
// zero value so a caller opts into either a stub or the real callModel path.
func newStandardCompactionFixture(
	t *testing.T,
	driver elelem.Driver,
) runtimeFixture {
	t.Helper()

	return newRuntimeFixtureWithOptions(t, driver, func(o *RuntimeOptions) {
		o.CompactionMode = config.CompactionModeSummarize
		o.MaxContextTokens = compactionTestMaxContextTokens
		o.CompactionOutputTokens = compactionTestOutputTokens
	})
}

func compactionUserText(turn int) string {
	return fmt.Sprintf(compactionUserTextFormat, turn)
}

func compactionAssistantText(turn int) string {
	return fmt.Sprintf(compactionAssistantTextFormat, turn)
}

// compactionScriptedTurns builds count ordinary scripted replies, one per
// Run call, each matching what compactionAssistantText returns for that turn
// number.
func compactionScriptedTurns(count int) []elelemtest.Turn {
	turns := make([]elelemtest.Turn, 0, count)

	for turn := 1; turn <= count; turn++ {
		turns = append(turns, elelemtest.Text(compactionAssistantText(turn)))
	}

	return turns
}

func runCompactionTurn(
	t *testing.T,
	fixture runtimeFixture,
	sessionID *uuid.UUID,
	message string,
) *TurnResult {
	t.Helper()

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: sessionID,
		Message:   message,
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	return result
}

// runCompactionTurns drives turns numbered from..to, resuming the session a
// previous call started when sessionID is non-nil.
func runCompactionTurns(
	t *testing.T,
	fixture runtimeFixture,
	sessionID *uuid.UUID,
	from, to int,
) (uuid.UUID, []*TurnResult) {
	t.Helper()

	results := make([]*TurnResult, 0, to-from+1)

	for turn := from; turn <= to; turn++ {
		result := runCompactionTurn(
			t,
			fixture,
			sessionID,
			compactionUserText(turn),
		)
		results = append(results, result)
		sessionID = &result.SessionID
	}

	return *sessionID, results
}

// runFixedCompactionTurns drives count turns carrying the exact same message
// text, for scenarios where the tokenizer must count real content bytes
// rather than a numbered placeholder.
func runFixedCompactionTurns(
	t *testing.T,
	fixture runtimeFixture,
	message string,
	count int,
) uuid.UUID {
	t.Helper()

	var sessionID *uuid.UUID

	var last uuid.UUID

	for range count {
		result := runCompactionTurn(t, fixture, sessionID, message)
		last = result.SessionID
		sessionID = &last
	}

	return last
}

// runFailingCompactionTurn drives a turn that requests a host tool, then
// fails on the following round, leaving a durable but incomplete assistant
// and tool-result pair behind a failed turn.
func runFailingCompactionTurn(
	t *testing.T,
	fixture runtimeFixture,
	sessionID uuid.UUID,
) {
	t.Helper()

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &sessionID,
		Message:   compactionFailedTurnUserText,
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, errCompactionTurnFailed)
}

func orderedMessages(
	t *testing.T,
	fixture runtimeFixture,
	sessionID uuid.UUID,
) []*models.Message {
	t.Helper()

	page, err := fixture.store.ListMessages(
		context.Background(),
		sessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	return page.Items
}

func allCompactionRows(
	t *testing.T,
	fixture runtimeFixture,
	sessionID uuid.UUID,
) []*models.Compaction {
	t.Helper()

	query := repositories.Use(fixture.handle.GormDB)
	rows, err := query.Compaction.WithContext(context.Background()).
		Where(query.Compaction.SessionID.Eq(sessionID)).
		Find()
	require.NoError(t, err)

	return rows
}

// otherCompactionRow returns whichever row in rows is not excludeID, failing
// the test when no such row exists.
func otherCompactionRow(
	t *testing.T,
	rows []*models.Compaction,
	excludeID uuid.UUID,
) *models.Compaction {
	t.Helper()

	for _, row := range rows {
		if row.ID != excludeID {
			return row
		}
	}

	require.FailNow(t, "expected a second compaction row")

	return nil
}

// assertSummaryInUserMessageNotSystem proves promptFromHistory's contract: a
// stored summary rides as user-role content, never inside the system
// message.
func assertSummaryInUserMessageNotSystem(
	t *testing.T,
	messages []elelem.Message,
	summaryText string,
) {
	t.Helper()

	require.NotEmpty(t, messages)
	assert.Equal(t, elelem.RoleSystem, messages[0].Role)
	assert.NotContains(t, messages[0].Text(), summaryText)

	found := false

	for _, message := range messages {
		if message.Role == elelem.RoleUser &&
			strings.Contains(message.Text(), summaryText) {
			found = true

			break
		}
	}

	assert.True(t, found, "expected the summary inside a user-role message")
}

// A transcript that never crosses the budget must never produce a stored
// summary; drop-oldest and summarize would otherwise be indistinguishable
// from below the line.
func TestCompactionBelowThresholdCreatesNoRow(t *testing.T) {
	scenario := setupCompactionScenario(t, 1)

	compaction, err := scenario.fixture.store.LatestCompaction(
		context.Background(),
		scenario.sessionID,
	)
	require.NoError(t, err)
	assert.Nil(t, compaction)
}

func TestCompactionFirstRunStoresShapedRow(t *testing.T) {
	scenario := setupCompactionScenario(t, compactionTurnsBeforeFirstTrigger+1)

	triggering := scenario.results[len(scenario.results)-1]
	assert.Equal(
		t,
		compactionAssistantText(compactionTurnsBeforeFirstTrigger+1),
		triggering.Text,
	)

	rows := allCompactionRows(t, scenario.fixture, scenario.sessionID)
	require.Len(t, rows, 1)
	compaction := rows[0]

	messages := orderedMessages(t, scenario.fixture, scenario.sessionID)
	require.Len(
		t,
		messages,
		(compactionTurnsBeforeFirstTrigger+1)*compactionOrdinaryTurnMessages,
	)

	assert.Equal(t, messages[0].ID, compaction.FromMessageID)
	assert.Equal(t, messages[0].Sequence, compaction.FromSequence)
	assert.Equal(t, messages[compactionFirstToIndex].ID, compaction.ToMessageID)
	assert.Equal(
		t,
		messages[compactionFirstToIndex].Sequence,
		compaction.ToSequence,
	)
	assert.Equal(
		t,
		int64(compactionFirstSourceMessageCount),
		compaction.SourceMessageCount,
	)
	assert.Equal(t, compactionScenarioSummaryText, compaction.Summary)
	assert.NotEmpty(t, compaction.PromptHash)
	assert.Nil(t, compaction.SupersedesCompactionID)
}

func TestCompactionHooksRunAroundStoredSummary(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		compactionScriptedTurns(compactionTurnsBeforeFirstTrigger + 1)...,
	).WithTokenCounter(compactionTokenCounter{
		perMessage: compactionTestTokensPerMessage,
	})
	fixture := newStandardCompactionFixture(t, driver)
	stub := newCompactionSummarizeStub(compactionScenarioSummaryText)
	fixture.runtime.compactionOptions.Summarize = stub.summarize

	type hookCall struct {
		event   harness.HookEvent
		payload compactionHookPayload
	}
	calls := make([]hookCall, 0, 2)
	fixture.runtime.compactionOptions.Hook = func(
		_ context.Context,
		event harness.HookEvent,
		payload compactionHookPayload,
	) error {
		calls = append(calls, hookCall{event: event, payload: payload})

		return nil
	}

	_, _ = runCompactionTurns(
		t,
		fixture,
		nil,
		1,
		compactionTurnsBeforeFirstTrigger+1,
	)

	require.Len(t, calls, 2)
	assert.Equal(t, harness.HookEventPreCompact, calls[0].event)
	assert.Equal(t, harness.HookEventPostCompact, calls[1].event)
	assert.Equal(t, fixture.workspace, calls[0].payload.Workspace)
	assert.Positive(t, calls[0].payload.EstimatedTokens)
	assert.Equal(t, compactionTestMaxContextTokens, calls[0].payload.BudgetTokens)
	assert.Positive(t, calls[0].payload.UnitsCovered)
	assert.Positive(t, calls[0].payload.MessagesCovered)
	assert.Zero(t, calls[0].payload.SummaryTokens)
	assert.Equal(
		t,
		int64(compactionStubOutputTokens),
		calls[1].payload.SummaryTokens,
	)
}

func TestCompactionPreHookFailureLeavesNoRow(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		compactionScriptedTurns(compactionTurnsBeforeFirstTrigger + 1)...,
	).WithTokenCounter(compactionTokenCounter{
		perMessage: compactionTestTokensPerMessage,
	})
	fixture := newStandardCompactionFixture(t, driver)
	stub := newCompactionSummarizeStub(compactionScenarioSummaryText)
	fixture.runtime.compactionOptions.Summarize = stub.summarize
	fixture.runtime.compactionOptions.Hook = func(
		_ context.Context,
		event harness.HookEvent,
		_ compactionHookPayload,
	) error {
		if event == harness.HookEventPreCompact {
			return errCompactionHookFailed
		}

		return nil
	}

	sessionID, _ := runCompactionTurns(
		t,
		fixture,
		nil,
		1,
		compactionTurnsBeforeFirstTrigger,
	)
	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &sessionID,
		Message:   compactionUserText(compactionTurnsBeforeFirstTrigger + 1),
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, errCompactionHookFailed)
	assert.Zero(t, stub.calls)

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	assert.Nil(t, compaction)
}

func TestCompactionPostHookFailureKeepsStoredSummary(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		compactionScriptedTurns(compactionTurnsBeforeFirstTrigger + 1)...,
	).WithTokenCounter(compactionTokenCounter{
		perMessage: compactionTestTokensPerMessage,
	})
	fixture := newStandardCompactionFixture(t, driver)
	stub := newCompactionSummarizeStub(compactionScenarioSummaryText)
	fixture.runtime.compactionOptions.Summarize = stub.summarize
	fixture.runtime.compactionOptions.Hook = func(
		_ context.Context,
		event harness.HookEvent,
		_ compactionHookPayload,
	) error {
		if event == harness.HookEventPostCompact {
			return errCompactionHookFailed
		}

		return nil
	}

	sessionID, results := runCompactionTurns(
		t,
		fixture,
		nil,
		1,
		compactionTurnsBeforeFirstTrigger+1,
	)
	assert.Equal(
		t,
		compactionAssistantText(compactionTurnsBeforeFirstTrigger+1),
		results[len(results)-1].Text,
	)
	assert.Equal(t, 1, stub.calls)

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	assert.NotNil(t, compaction)
}

// Drop-oldest installs no Peen hook at all, so Elelem's own whole-unit
// eviction must run the request to completion without ever touching the
// compactions table.
func TestCompactionDropOldestModeStoresNothing(t *testing.T) {
	runs := compactionTurnsBeforeFirstTrigger + 1
	driver := elelemtest.NewScriptedDriver(compactionScriptedTurns(runs)...).
		WithTokenCounter(compactionTokenCounter{
			perMessage: compactionTestTokensPerMessage,
		})

	fixture := newRuntimeFixtureWithOptions(t, driver, func(o *RuntimeOptions) {
		o.MaxContextTokens = compactionTestMaxContextTokens
		o.CompactionOutputTokens = compactionTestOutputTokens
	})

	sessionID, results := runCompactionTurns(t, fixture, nil, 1, runs)
	assert.Equal(t, compactionAssistantText(runs), results[len(results)-1].Text)

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	assert.Nil(t, compaction)
}

// A second over-budget run must supersede the first row rather than
// competing with it, and the reload path must keep the stored summary as
// user-role content.
func TestCompactionRepeatedSupersedingPreservesHistoryAndPlacement(
	t *testing.T,
) {
	scenario := setupCompactionScenario(t, compactionTurnsBeforeSecondTrigger)

	latest, err := scenario.fixture.store.LatestCompaction(
		context.Background(),
		scenario.sessionID,
	)
	require.NoError(t, err)
	require.NotNil(t, latest)

	rows := allCompactionRows(t, scenario.fixture, scenario.sessionID)
	require.Len(t, rows, 2)
	first := otherCompactionRow(t, rows, latest.ID)

	messages := orderedMessages(t, scenario.fixture, scenario.sessionID)
	require.Len(
		t,
		messages,
		compactionTurnsBeforeSecondTrigger*compactionOrdinaryTurnMessages,
	)

	assert.Equal(t, messages[0].ID, latest.FromMessageID)
	assert.Equal(t, messages[compactionSecondToIndex].ID, latest.ToMessageID)
	assert.Equal(
		t,
		int64(compactionSecondSourceMessageCount),
		latest.SourceMessageCount,
	)
	require.NotNil(t, latest.SupersedesCompactionID)
	assert.Equal(t, first.ID, *latest.SupersedesCompactionID)

	for index := 0; index <= compactionFirstToIndex; index++ {
		require.NotNil(t, messages[index].CompactionID)
		assert.Equal(t, first.ID, *messages[index].CompactionID)
	}
	for index := compactionFirstToIndex + 1; index <= compactionSecondToIndex; index++ {
		require.NotNil(t, messages[index].CompactionID)
		assert.Equal(t, latest.ID, *messages[index].CompactionID)
	}
	for index := compactionSecondToIndex + 1; index < len(messages); index++ {
		assert.Nil(t, messages[index].CompactionID)
	}

	request, ok := scenario.driver.LastRequest()
	require.True(t, ok)
	assertSummaryInUserMessageNotSystem(
		t,
		request.Messages,
		compactionScenarioSummaryText,
	)
}

// Compaction only ever ADDS rows. The original transcript must survive both
// compactions byte for byte, and none of it may carry the synthetic summary
// lead a GET /v1/messages caller must never see.
func TestCompactionOriginalTranscriptNeverMutated(t *testing.T) {
	scenario := setupCompactionScenario(t, compactionTurnsBeforeSecondTrigger)

	messages := orderedMessages(t, scenario.fixture, scenario.sessionID)
	require.Len(
		t,
		messages,
		compactionTurnsBeforeSecondTrigger*compactionOrdinaryTurnMessages,
	)

	for turn := 1; turn <= compactionTurnsBeforeSecondTrigger; turn++ {
		userIndex := (turn - 1) * compactionOrdinaryTurnMessages
		assistantIndex := userIndex + 1

		assert.Equal(t, compactionUserText(turn), messages[userIndex].Content)
		assert.Equal(
			t,
			compactionAssistantText(turn),
			messages[assistantIndex].Content,
		)
		assert.NotContains(
			t,
			messages[userIndex].Content,
			compactionSummaryLead,
		)
		assert.NotContains(
			t,
			messages[assistantIndex].Content,
			compactionSummaryLead,
		)
	}
}

// A tool-call unit (assistant-with-tool-calls plus its tool result) must
// never be split by a compaction boundary: the stored range either stops
// before the assistant row or reaches at least the last tool row.
func TestCompactionRespectsToolCallUnitBoundary(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			compactionToolBoundaryCallID,
			toolNameReadFile,
			runtimeToolArguments(t, compactionAbsentFileName),
		),
		elelemtest.Text(compactionToolBoundaryFinalText),
		elelemtest.Text(compactionAssistantText(2)),
		elelemtest.Text(compactionAssistantText(3)),
		elelemtest.Text(compactionAssistantText(4)),
		elelemtest.Text(compactionAssistantText(5)),
	).WithTokenCounter(compactionTokenCounter{
		perMessage: compactionTestTokensPerMessage,
	})

	fixture := newRuntimeFixtureWithOptions(t, driver, func(o *RuntimeOptions) {
		o.CompactionMode = config.CompactionModeSummarize
		o.MaxContextTokens = compactionToolBoundaryMaxContextTokens
		o.CompactionOutputTokens = compactionToolBoundaryOutputTokens
	})

	stub := newCompactionSummarizeStub(compactionScenarioSummaryText)
	fixture.runtime.compactionOptions.Summarize = stub.summarize

	result1 := runCompactionTurn(t, fixture, nil, compactionUserText(1))
	assert.Equal(t, compactionToolBoundaryFinalText, result1.Text)

	sessionID := result1.SessionID
	_, _ = runCompactionTurns(t, fixture, &sessionID, 2, 4)
	result5 := runCompactionTurn(t, fixture, &sessionID, compactionUserText(5))
	assert.Equal(t, compactionAssistantText(5), result5.Text)

	messages := orderedMessages(t, fixture, sessionID)
	require.Len(t, messages, compactionToolBoundaryMessageCount)

	assistantToolMessage := messages[compactionToolBoundaryAssistantIndex]
	toolResultMessage := messages[compactionToolBoundaryResultIndex]

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	require.NotNil(t, compaction)

	respectsUnit := compaction.ToSequence < assistantToolMessage.Sequence ||
		compaction.ToSequence >= toolResultMessage.Sequence
	assert.True(t, respectsUnit, "compaction must not split a tool call unit")
}

// A failed turn's messages must never be summarized nor named by a later
// compaction's covered range, even though their durable rows persist and get
// marked incomplete.
func TestCompactionExcludesFailedTurnMessages(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text(compactionAssistantText(1)),
		elelemtest.ToolCall(
			compactionFailedToolCallID,
			toolNameReadFile,
			runtimeToolArguments(t, compactionAbsentFileName),
		),
		elelemtest.Turn{Err: errCompactionTurnFailed},
		elelemtest.Text(compactionAssistantText(3)),
		elelemtest.Text(compactionAssistantText(4)),
		elelemtest.Text(compactionAssistantText(5)),
	).WithTokenCounter(compactionTokenCounter{
		perMessage: compactionTestTokensPerMessage,
	})
	fixture := newStandardCompactionFixture(t, driver)

	stub := newCompactionSummarizeStub(compactionScenarioSummaryText)
	fixture.runtime.compactionOptions.Summarize = stub.summarize

	sessionID, _ := runCompactionTurns(t, fixture, nil, 1, 1)
	runFailingCompactionTurn(t, fixture, sessionID)

	_, results := runCompactionTurns(t, fixture, &sessionID, 3, 5)
	assert.Equal(t, compactionAssistantText(5), results[len(results)-1].Text)

	messages := orderedMessages(t, fixture, sessionID)
	require.Len(t, messages, compactionFailedMessageCount)

	assert.False(t, messages[compactionFailedTurnUserIndex].Incomplete)
	assert.True(t, messages[compactionFailedTurnAssistantIndex].Incomplete)
	assert.True(t, messages[compactionFailedTurnToolIndex].Incomplete)

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	require.NotNil(t, compaction)
	assert.Equal(t, messages[0].ID, compaction.FromMessageID)
	assert.Equal(t, messages[compactionFailedToIndex].ID, compaction.ToMessageID)
	assert.Equal(
		t,
		int64(compactionFirstSourceMessageCount),
		compaction.SourceMessageCount,
	)

	transcript := stub.lastTranscript()
	assert.NotContains(t, transcript, compactionFailedTurnUserText)
	assert.NotContains(t, transcript, toolNameReadFile)
}

func TestCompactionSummarizerErrorLeavesNoRow(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		compactionScriptedTurns(compactionTurnsBeforeFirstTrigger + 1)...,
	).WithTokenCounter(compactionTokenCounter{
		perMessage: compactionTestTokensPerMessage,
	})
	fixture := newStandardCompactionFixture(t, driver)

	stub := newCompactionSummarizeStub("")
	stub.err = errCompactionSummarizeFailed
	fixture.runtime.compactionOptions.Summarize = stub.summarize

	sessionID, _ := runCompactionTurns(
		t,
		fixture,
		nil,
		1,
		compactionTurnsBeforeFirstTrigger,
	)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &sessionID,
		Message:   compactionUserText(compactionTurnsBeforeFirstTrigger + 1),
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, errCompactionSummarizeFailed)

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	assert.Nil(t, compaction)
}

func TestCompactionEmptySummaryLeavesNoRow(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		compactionScriptedTurns(compactionTurnsBeforeFirstTrigger + 1)...,
	).WithTokenCounter(compactionTokenCounter{
		perMessage: compactionTestTokensPerMessage,
	})
	fixture := newStandardCompactionFixture(t, driver)

	stub := newCompactionSummarizeStub(compactionWhitespaceSummaryText)
	fixture.runtime.compactionOptions.Summarize = stub.summarize

	sessionID, _ := runCompactionTurns(
		t,
		fixture,
		nil,
		1,
		compactionTurnsBeforeFirstTrigger,
	)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &sessionID,
		Message:   compactionUserText(compactionTurnsBeforeFirstTrigger + 1),
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, ErrCompactionEmptySummary)

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	assert.Nil(t, compaction)
}

// A per-message counter alone can never distinguish selectPrefix's short
// placeholder from commit's real summary text, so this test uses a
// byte-weighted counter (see the compactionInsufficient* constants) and an
// intentionally oversized stub summary to force the post-commit recount back
// over budget.
func TestCompactionInsufficientSummaryLeavesNoRow(t *testing.T) {
	probe := newRuntimeFixtureWithOptions(t, elelemtest.NewScriptedDriver(), nil)
	snapshot, err := probe.runtime.resolver.Resolve(probe.workspace)
	require.NoError(t, err)
	systemPrompt, err := probe.runtime.systemPrompt(
		snapshot,
		TurnRequest{},
		probe.workspace,
	)
	require.NoError(t, err)

	maxContextTokens := len(systemPrompt) + compactionInsufficientBudgetMargin
	message := strings.Repeat(
		compactionInsufficientMessageChar,
		compactionInsufficientMessageBytes,
	)

	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text(message),
		elelemtest.Text(message),
		elelemtest.Text(message),
		elelemtest.Text(message),
	).WithTokenCounter(compactionTokenCounter{perByte: 1})

	fixture := newRuntimeFixtureWithOptions(t, driver, func(o *RuntimeOptions) {
		o.CompactionMode = config.CompactionModeSummarize
		o.MaxContextTokens = maxContextTokens
		o.CompactionOutputTokens = compactionInsufficientOutputTokens
	})

	stub := newCompactionSummarizeStub(strings.Repeat(
		compactionInsufficientSummaryChar,
		compactionInsufficientSummaryBytes,
	))
	fixture.runtime.compactionOptions.Summarize = stub.summarize

	sessionID := runFixedCompactionTurns(
		t,
		fixture,
		message,
		compactionTurnsBeforeFirstTrigger,
	)

	_, err = fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &sessionID,
		Message:   message,
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, ErrCompactionInsufficient)

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	assert.Nil(t, compaction)
}

// Once every completed message belongs to the newest turn, nothing is
// eligible to compact: the hook must fail closed without ever asking the
// summarizer to run.
func TestCompactionNoEligiblePrefixLeavesNoRow(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(compactionScriptedTurns(1)...).
		WithTokenCounter(compactionTokenCounter{
			perMessage: compactionTestTokensPerMessage,
		})

	fixture := newRuntimeFixtureWithOptions(t, driver, func(o *RuntimeOptions) {
		o.CompactionMode = config.CompactionModeSummarize
		o.MaxContextTokens = compactionUnavailableMaxContextTokens
		o.CompactionOutputTokens = compactionUnavailableOutputTokens
	})

	stub := newCompactionSummarizeStub(compactionScenarioSummaryText)
	fixture.runtime.compactionOptions.Summarize = stub.summarize

	sessionID, _ := runCompactionTurns(t, fixture, nil, 1, 1)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &sessionID,
		Message:   compactionUserText(2),
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, ErrCompactionUnavailable)
	assert.Zero(t, stub.calls)

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	assert.Nil(t, compaction)
}

// Leaving Summarize nil must route the compactor through the real callModel
// path, against the same scripted driver as the deployment's own model.
func TestCompactionRealCallModelPopulatesModelID(t *testing.T) {
	scripted := compactionScriptedTurns(compactionTurnsBeforeFirstTrigger)
	scripted = append(
		scripted,
		elelemtest.Text(compactionCallModelSummaryText),
		elelemtest.Text(
			compactionAssistantText(compactionTurnsBeforeFirstTrigger+1),
		),
	)

	driver := elelemtest.NewScriptedDriver(scripted...).
		WithTokenCounter(compactionTokenCounter{
			perMessage: compactionTestTokensPerMessage,
		})
	fixture := newStandardCompactionFixture(t, driver)

	sessionID, _ := runCompactionTurns(
		t,
		fixture,
		nil,
		1,
		compactionTurnsBeforeFirstTrigger,
	)
	result := runCompactionTurn(
		t,
		fixture,
		&sessionID,
		compactionUserText(compactionTurnsBeforeFirstTrigger+1),
	)
	assert.Equal(
		t,
		compactionAssistantText(compactionTurnsBeforeFirstTrigger+1),
		result.Text,
	)

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	require.NotNil(t, compaction)
	assert.Equal(t, runtimeTestModelID, compaction.ModelID)
	assert.Equal(t, compactionCallModelSummaryText, compaction.Summary)
}

// The same session reconstructed through a second Store over the same handle
// must see the same active compaction and the same uncovered raw tail.
func TestCompactionSurvivesRestartWithSecondStore(t *testing.T) {
	scenario := setupCompactionScenario(t, compactionTurnsBeforeFirstTrigger+1)
	ctx := context.Background()

	history1, err := scenario.fixture.store.CompletedHistory(
		ctx,
		scenario.sessionID,
	)
	require.NoError(t, err)
	require.NotNil(t, history1.Compaction)

	store2, err := session.NewStore(scenario.fixture.handle, session.Options{})
	require.NoError(t, err)

	history2, err := store2.CompletedHistory(ctx, scenario.sessionID)
	require.NoError(t, err)

	assert.Equal(t, history1.Compaction, history2.Compaction)
	assert.Equal(t, history1.Messages, history2.Messages)
}

// Compacting one session must never touch another session sharing the same
// store and runtime.
func TestCompactionIsolatedPerSession(t *testing.T) {
	total := compactionSessionIsolationRunsA + compactionSessionIsolationRunsB
	driver := elelemtest.NewScriptedDriver(compactionScriptedTurns(total)...).
		WithTokenCounter(compactionTokenCounter{
			perMessage: compactionTestTokensPerMessage,
		})
	fixture := newStandardCompactionFixture(t, driver)

	stub := newCompactionSummarizeStub(compactionScenarioSummaryText)
	fixture.runtime.compactionOptions.Summarize = stub.summarize

	sessionA, _ := runCompactionTurns(
		t,
		fixture,
		nil,
		1,
		compactionSessionIsolationRunsA,
	)
	sessionB, _ := runCompactionTurns(
		t,
		fixture,
		nil,
		compactionSessionIsolationRunsA+1,
		total,
	)

	ctx := context.Background()
	compactionA, err := fixture.store.LatestCompaction(ctx, sessionA)
	require.NoError(t, err)
	assert.NotNil(t, compactionA)

	compactionB, err := fixture.store.LatestCompaction(ctx, sessionB)
	require.NoError(t, err)
	assert.Nil(t, compactionB)

	historyB, err := fixture.store.CompletedHistory(ctx, sessionB)
	require.NoError(t, err)
	assert.Nil(t, historyB.Compaction)
	assert.Equal(t, orderedMessages(t, fixture, sessionB), historyB.Messages)
}
