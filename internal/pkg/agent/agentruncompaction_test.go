package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// childCompactionTokensPerMessage is the flat per-message charge the test
	// token counter applies, so message count times this constant is the
	// child prompt's estimated cost.
	childCompactionTokensPerMessage = 100

	// childCompactionRepeatBudget leaves room for a few child rounds and then
	// forces a fresh compaction on nearly every round after that, which is
	// what produces a chain rather than a single row.
	childCompactionRepeatBudget = 900

	// childCompactionSingleBudget is tuned so exactly one child compaction
	// fires, on the child's third request, keeping the scripted driver's turn
	// order deterministic when the real compaction model path consumes one.
	childCompactionSingleBudget = 550

	childCompactionOutputTokens = 50
	childCompactionRounds       = 8

	// childCompactionFailureRounds is the largest round count whose next
	// request is the one over budget, so a compaction that fails ends the
	// child instead of handing its remaining scripted turns to the parent.
	childCompactionFailureRounds = 4

	childCompactionReadFile     = "child-readable.txt"
	childCompactionFileContent  = "child readable content"
	childCompactionTask         = "review the file across rounds"
	childCompactionInjectMark   = "CHILDMARK-injected-note"
	childCompactionSummaryLead  = "child summary "
	childCompactionModelSummary = "summary produced by the compaction model"

	childCompactionFinished   = "child finished"
	childCompactionParentText = "parent done"
	childCompactionSeedText   = "parent turn one answer"

	// childCompactionScriptMode is owner read/write/execute: the hook command
	// runs as the test process and nothing else needs to reach it.
	childCompactionScriptMode = 0o700

	childCompactionPageSize = int32(2)
)

var errChildCompactionSummarize = errors.New(
	"child compaction test summarizer failed",
)

// childCompactionCounter is a deterministic elelem.TokenCounter, so the child
// budget arithmetic these tests rely on is message count rather than tokenizer
// behavior.
type childCompactionCounter struct{}

func (childCompactionCounter) Count(
	messages []elelem.Message,
	_ []elelem.Tool,
) (int, error) {
	return len(messages) * childCompactionTokensPerMessage, nil
}

// childCompactionSummarizer stands in for the compaction model. It counts the
// summaries it produced and runs an optional probe at the moment a child
// compaction is about to be committed.
type childCompactionSummarizer struct {
	count  int
	before func()
}

func (s *childCompactionSummarizer) summarize(
	_ context.Context,
	_ string,
) (compactionSummary, error) {
	s.count++

	if s.before != nil {
		s.before()
	}

	return compactionSummary{
		Text:         fmt.Sprintf("%s%d", childCompactionSummaryLead, s.count),
		ModelID:      runtimeTestModelID,
		InputTokens:  childCompactionRepeatBudget,
		OutputTokens: childCompactionOutputTokens,
	}, nil
}

// childCompactionFixture builds a launch-agent runtime in summarize mode with
// a deterministic token counter and the given context budget.
func childCompactionFixture(
	t *testing.T,
	driver elelem.Driver,
	budget int,
) runtimeFixture {
	t.Helper()

	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentRestrictedChildAgentFile,
		},
		Customize: func(o *RuntimeOptions) {
			o.CompactionMode = config.CompactionModeSummarize
			o.MaxContextTokens = budget
			o.CompactionOutputTokens = childCompactionOutputTokens
		},
	})

	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, childCompactionReadFile),
		childCompactionFileContent,
	)

	return fixture
}

// childCompactionTurns scripts a parent that delegates once, a child that runs
// rounds tool rounds and then answers, and the parent's closing message.
func childCompactionTurns(t *testing.T, rounds int) []elelemtest.Turn {
	t.Helper()

	turns := make([]elelemtest.Turn, 0, rounds+3)
	turns = append(turns, elelemtest.ToolCall(
		launchAgentCallID,
		toolNameLaunchAgent,
		launchAgentArguments(t, launchAgentInput{
			Task:  childCompactionTask,
			Agent: launchAgentChildName,
		}),
	))

	for round := range rounds {
		turns = append(turns, elelemtest.ToolCall(
			fmt.Sprintf("call_child_round_%d", round),
			toolNameReadFile,
			runtimeToolArguments(t, childCompactionReadFile),
		))
	}

	return append(
		turns,
		elelemtest.Text(childCompactionFinished),
		elelemtest.Text(childCompactionParentText),
	)
}

func childCompactionDriver(
	t *testing.T,
	rounds int,
) *elelemtest.ScriptedDriver {
	t.Helper()

	return elelemtest.NewScriptedDriver(childCompactionTurns(t, rounds)...).
		WithTokenCounter(childCompactionCounter{})
}

// writeChildCompactionInjectHook makes every child tool round add one injected
// message to the child conversation.
func writeChildCompactionInjectHook(t *testing.T, fixture runtimeFixture) {
	t.Helper()

	writeChildHarnessHooks(t, fixture, `version: 1
post_read_file:
  - name: child-note
    actions:
      - type: inject
        message: `+childCompactionInjectMark+`
`)
}

func runChildCompactionTurn(
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

// onlyChildRun returns the single child agent run the turn launched.
func onlyChildRun(
	t *testing.T,
	fixture runtimeFixture,
	sessionID uuid.UUID,
) *AgentRun {
	t.Helper()

	registry, err := fixture.runtime.sessionAgentRuns(sessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 1)

	return runs[0]
}

// childCompactionChain walks a child's compaction lineage from its head
// backwards through SupersedesCompactionID, returning it oldest first.
//
// The chain is walked rather than listed, because the superseding link is the
// relation under test and list order is creation time.
func childCompactionChain(
	t *testing.T,
	store *session.Store,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) []*models.AgentRunCompaction {
	t.Helper()

	ctx := context.Background()

	head, err := store.LatestAgentRunCompaction(ctx, sessionID, agentRunID)
	require.NoError(t, err)

	chain := []*models.AgentRunCompaction{}

	for current := head; current != nil; {
		chain = append([]*models.AgentRunCompaction{current}, chain...)

		if current.SupersedesCompactionID == nil {
			break
		}

		previous, getErr := store.GetAgentRunCompaction(
			ctx,
			sessionID,
			agentRunID,
			*current.SupersedesCompactionID,
		)
		require.NoError(t, getErr)

		current = previous
	}

	return chain
}

// A child agent is a separate model context, so it keeps its own durable
// transcript: the task as a user message, then every prompt-visible assistant
// message, tool result, and injection.
//
// Compacting that transcript repeatedly must extend a lineage rather than
// rewrite it, so each row keeps the direct membership it recorded and points
// at its direct predecessor.
func TestChildAgentKeepsOwnTranscriptAndCompactionLineage(t *testing.T) {
	ctx := context.Background()
	driver := childCompactionDriver(t, childCompactionRounds)
	fixture := childCompactionFixture(t, driver, childCompactionRepeatBudget)
	writeChildCompactionInjectHook(t, fixture)

	summarizer := &childCompactionSummarizer{}
	fixture.runtime.compactionOptions.Summarize = summarizer.summarize

	result := runChildCompactionTurn(t, fixture)
	run := onlyChildRun(t, fixture, result.SessionID)

	messages, err := fixture.store.ListAgentRunMessages(
		ctx,
		result.SessionID,
		run.ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)
	require.NotEmpty(t, messages.Items)

	first := messages.Items[0]
	assert.Equal(
		t,
		models.MessageRoleUser,
		first.Role,
		"a child transcript opens with its task",
	)
	assert.Equal(t, childCompactionTask, first.Content)
	assert.Equal(t, int64(1), first.Sequence)

	roles := map[models.MessageRole]int{}
	injected := 0

	for index, message := range messages.Items {
		assert.Equal(t, run.ID, message.AgentRunID)
		assert.Equal(t, result.SessionID, message.SessionID)
		assert.Equal(
			t,
			int64(index+1),
			message.Sequence,
			"child sequences are dense and per-run",
		)

		roles[message.Role]++

		if strings.Contains(message.Content, childCompactionInjectMark) {
			injected++
		}
	}

	assert.Positive(
		t,
		roles[models.MessageRoleAssistant],
		"child assistant messages must be durable",
	)
	assert.Positive(
		t,
		roles[models.MessageRoleTool],
		"child tool results must be durable",
	)
	assert.Positive(t, injected, "child injections must be durable")

	chain := childCompactionChain(t, fixture.store, result.SessionID, run.ID)
	require.GreaterOrEqual(
		t,
		len(chain),
		2,
		"the child must compact more than once to prove a lineage",
	)
	assert.Len(t, chain, summarizer.count)
	assert.Nil(
		t,
		chain[0].SupersedesCompactionID,
		"the first child compaction supersedes nothing",
	)

	for index, compaction := range chain {
		assert.Equal(t, run.ID, compaction.AgentRunID)
		assert.Equal(t, result.SessionID, compaction.SessionID)
		assert.LessOrEqual(
			t,
			compaction.DirectFromSequence,
			compaction.DirectToSequence,
		)
		assert.Equal(
			t,
			chain[0].FromSequence,
			compaction.FromSequence,
			"the lineage span starts at the first covered message",
		)

		if index == 0 {
			continue
		}

		previous := chain[index-1]

		require.NotNil(t, compaction.SupersedesCompactionID)
		assert.Equal(
			t,
			previous.ID,
			*compaction.SupersedesCompactionID,
			"a later child compaction points at its direct predecessor",
		)
		assert.Greater(
			t,
			compaction.DirectFromSequence,
			previous.DirectToSequence,
			"direct membership never re-covers an earlier compaction",
		)
		assert.Greater(
			t,
			compaction.SourceMessageCount,
			previous.SourceMessageCount,
			"the lineage grows as it supersedes",
		)
	}

	// Direct membership is also what the message rows record, so the two
	// views have to agree.
	assigned := map[uuid.UUID]int64{}

	for _, message := range messages.Items {
		if message.CompactionID == nil {
			continue
		}

		assigned[*message.CompactionID]++
	}

	for _, compaction := range chain {
		assert.Equal(
			t,
			compaction.DirectToSequence-compaction.DirectFromSequence+1,
			assigned[compaction.ID],
			"every message in a direct range carries that compaction id",
		)
	}
}

// A child compaction belongs to the child alone. Whatever the parent session
// had stored when the child compacted must still read back identically, and no
// parent message may be claimed by a child compaction.
func TestChildCompactionLeavesParentRecordsUntouched(t *testing.T) {
	ctx := context.Background()
	turns := append(
		[]elelemtest.Turn{elelemtest.Text(childCompactionSeedText)},
		childCompactionTurns(t, childCompactionRounds)...,
	)
	driver := elelemtest.NewScriptedDriver(turns...).
		WithTokenCounter(childCompactionCounter{})
	fixture := childCompactionFixture(t, driver, childCompactionRepeatBudget)

	// One ordinary turn first, so the parent already has durable rows before
	// any child compaction runs and the snapshot below is not empty.
	seed, err := fixture.runtime.Run(ctx, TurnRequest{
		Message:   "answer directly",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	require.Equal(t, childCompactionSeedText, seed.Text)

	type parentRow struct {
		content      string
		compactionID *uuid.UUID
	}

	snapshot := map[uuid.UUID]parentRow{}
	parentCompactionsWhenChildCompacted := 0

	summarizer := &childCompactionSummarizer{}
	summarizer.before = func() {
		if len(snapshot) > 0 {
			return
		}

		page, listErr := fixture.store.ListMessages(
			ctx,
			seed.SessionID,
			session.ListMessagesOptions{
				Limit: session.MaximumPageLimit,
				Order: session.PageOrderAscending,
			},
		)
		require.NoError(t, listErr)

		for _, message := range page.Items {
			snapshot[message.ID] = parentRow{
				content:      message.Content,
				compactionID: message.CompactionID,
			}
		}

		compactions, listErr := fixture.store.ListCompactions(
			ctx,
			seed.SessionID,
			session.ListCompactionsOptions{Limit: session.MaximumPageLimit},
		)
		require.NoError(t, listErr)

		parentCompactionsWhenChildCompacted = len(compactions.Items)
	}

	fixture.runtime.compactionOptions.Summarize = summarizer.summarize

	result := runChildCompactionTurn(t, fixture)
	run := onlyChildRun(t, fixture, result.SessionID)

	require.Positive(t, summarizer.count, "the child never compacted")
	require.NotEmpty(t, snapshot, "the parent had no rows to protect")

	after, err := fixture.store.ListMessages(
		ctx,
		result.SessionID,
		session.ListMessagesOptions{
			Limit: session.MaximumPageLimit,
			Order: session.PageOrderAscending,
		},
	)
	require.NoError(t, err)

	survivors := 0

	for _, message := range after.Items {
		original, ok := snapshot[message.ID]
		if !ok {
			continue
		}

		survivors++

		assert.Equal(
			t,
			original.content,
			message.Content,
			"a parent message must not be rewritten by child compaction",
		)
		assert.Equal(
			t,
			original.compactionID,
			message.CompactionID,
			"child compaction must not claim a parent message",
		)
	}

	assert.Equal(t, len(snapshot), survivors, "a parent message disappeared")

	parentCompactions, err := fixture.store.ListCompactions(
		ctx,
		result.SessionID,
		session.ListCompactionsOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)
	assert.Len(
		t,
		parentCompactions.Items,
		parentCompactionsWhenChildCompacted,
		"child compaction must not add a session compaction row",
	)

	childIDs := map[uuid.UUID]struct{}{}
	for _, compaction := range childCompactionChain(
		t,
		fixture.store,
		result.SessionID,
		run.ID,
	) {
		childIDs[compaction.ID] = struct{}{}
	}

	require.NotEmpty(t, childIDs)

	for _, message := range after.Items {
		assert.NotContains(
			t,
			message.Content,
			childCompactionSummaryLead,
			"a child summary must not enter the parent transcript",
		)

		if message.CompactionID == nil {
			continue
		}

		assert.NotContains(t, childIDs, *message.CompactionID)
	}
}

// The compaction model call a child makes stays attributable to its session
// and its parent turn, and carries the child's AgentRunID, so child compaction
// work is separable from the parent turn's own.
func TestChildCompactionModelRunCarriesAgentRunID(t *testing.T) {
	ctx := context.Background()

	// Leaving Summarize nil routes the compactor through the real callModel
	// path, which is what records a ModelRun. Under the single-compaction
	// budget the child's third request is the one over budget, so the
	// compaction model reads the text turn scripted for it here.
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  childCompactionTask,
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.ToolCall(
			"call_child_round_0",
			toolNameReadFile,
			runtimeToolArguments(t, childCompactionReadFile),
		),
		elelemtest.ToolCall(
			"call_child_round_1",
			toolNameReadFile,
			runtimeToolArguments(t, childCompactionReadFile),
		),
		elelemtest.Text(childCompactionModelSummary),
		elelemtest.Text(childCompactionFinished),
		elelemtest.Text(childCompactionParentText),
	).WithTokenCounter(childCompactionCounter{})

	fixture := childCompactionFixture(t, driver, childCompactionSingleBudget)

	result := runChildCompactionTurn(t, fixture)
	run := onlyChildRun(t, fixture, result.SessionID)

	head, err := fixture.store.LatestAgentRunCompaction(
		ctx,
		result.SessionID,
		run.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, head, "the child never compacted")
	assert.Equal(t, childCompactionModelSummary, head.Summary)
	assert.Equal(t, runtimeTestModelID, head.ModelID)

	query := repositories.Use(fixture.handle.GormDB)

	compactionRuns, err := query.ModelRun.WithContext(ctx).
		Where(
			query.ModelRun.SessionID.Eq(result.SessionID),
			query.ModelRun.Stage.Eq(string(models.ModelRunStageCompaction)),
		).
		Find()
	require.NoError(t, err)
	require.NotEmpty(t, compactionRuns, "the child recorded no audit")

	turn, err := query.Turn.WithContext(ctx).
		Where(query.Turn.SessionID.Eq(result.SessionID)).
		First()
	require.NoError(t, err)

	for _, modelRun := range compactionRuns {
		assert.Equal(
			t,
			result.SessionID,
			modelRun.SessionID,
			"a child compaction audit keeps session attribution",
		)
		assert.Equal(
			t,
			turn.ID,
			modelRun.TurnID,
			"a child compaction audit keeps parent-turn attribution",
		)
		require.NotNil(
			t,
			modelRun.AgentRunID,
			"a child compaction audit must set AgentRunID",
		)
		assert.Equal(t, run.ID, *modelRun.AgentRunID)
	}

	// The parent turn's own model runs stay unattributed to any child, which
	// is what makes the child's separable.
	turnRuns, err := query.ModelRun.WithContext(ctx).
		Where(
			query.ModelRun.SessionID.Eq(result.SessionID),
			query.ModelRun.Stage.Eq(string(models.ModelRunStageTurn)),
		).
		Find()
	require.NoError(t, err)
	require.NotEmpty(t, turnRuns)

	for _, modelRun := range turnRuns {
		assert.Nil(t, modelRun.AgentRunID)
	}
}

// Replay is the point of storing any of this. Reopening the same SQLite file
// has to return the child transcript, the direct membership each compaction
// recorded, and the whole superseding chain.
func TestChildTranscriptAndCompactionsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	driver := childCompactionDriver(t, childCompactionRounds)
	fixture := childCompactionFixture(t, driver, childCompactionRepeatBudget)
	writeChildCompactionInjectHook(t, fixture)

	summarizer := &childCompactionSummarizer{}
	fixture.runtime.compactionOptions.Summarize = summarizer.summarize

	result := runChildCompactionTurn(t, fixture)
	run := onlyChildRun(t, fixture, result.SessionID)

	before, err := fixture.store.ListAgentRunMessages(
		ctx,
		result.SessionID,
		run.ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)
	require.NotEmpty(t, before.Items)

	beforeChain := childCompactionChain(
		t,
		fixture.store,
		result.SessionID,
		run.ID,
	)
	require.GreaterOrEqual(t, len(beforeChain), 2)

	require.NoError(t, fixture.handle.Close())

	reopened, err := db.Open(ctx, db.Config{Directory: fixture.stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	restored, err := session.NewStore(reopened, session.Options{})
	require.NoError(t, err)

	after, err := restored.ListAgentRunMessages(
		ctx,
		result.SessionID,
		run.ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)
	require.Len(t, after.Items, len(before.Items))

	for index, message := range after.Items {
		original := before.Items[index]

		assert.Equal(t, original.ID, message.ID)
		assert.Equal(t, original.Sequence, message.Sequence)
		assert.Equal(t, original.Role, message.Role)
		assert.Equal(t, original.Content, message.Content)
		assert.Equal(t, original.ToolCallID, message.ToolCallID)
		assert.Equal(
			t,
			original.CompactionID,
			message.CompactionID,
			"direct membership must survive a restart",
		)
	}

	afterChain := childCompactionChain(t, restored, result.SessionID, run.ID)
	require.Len(t, afterChain, len(beforeChain))

	for index, compaction := range afterChain {
		original := beforeChain[index]

		assert.Equal(t, original.ID, compaction.ID)
		assert.Equal(t, original.Summary, compaction.Summary)
		assert.Equal(t, original.FromSequence, compaction.FromSequence)
		assert.Equal(t, original.ToSequence, compaction.ToSequence)
		assert.Equal(
			t,
			original.DirectFromSequence,
			compaction.DirectFromSequence,
		)
		assert.Equal(t, original.DirectToSequence, compaction.DirectToSequence)
		assert.Equal(
			t,
			original.SupersedesCompactionID,
			compaction.SupersedesCompactionID,
			"the superseding chain must survive a restart",
		)
	}

	// A resumed child reads its history through the same path the compactor
	// does, so that has to reconstruct too.
	history, err := restored.AgentRunHistory(ctx, result.SessionID, run.ID)
	require.NoError(t, err)
	require.NotNil(t, history.Compaction)
	assert.Equal(
		t,
		afterChain[len(afterChain)-1].ID,
		history.Compaction.ID,
		"replay resumes from the newest compaction",
	)

	for _, message := range history.Messages {
		assert.Nil(
			t,
			message.CompactionID,
			"replay returns only the uncovered raw tail",
		)
		assert.Greater(t, message.Sequence, history.Compaction.ToSequence)
	}
}

// The session-scoped read routes must refuse another session's child records
// and page within their bounds.
func TestChildTranscriptRoutesIsolateSessionsAndPage(t *testing.T) {
	ctx := context.Background()
	driver := childCompactionDriver(t, childCompactionRounds)
	fixture := childCompactionFixture(t, driver, childCompactionRepeatBudget)
	writeChildCompactionInjectHook(t, fixture)

	summarizer := &childCompactionSummarizer{}
	fixture.runtime.compactionOptions.Summarize = summarizer.summarize

	result := runChildCompactionTurn(t, fixture)
	run := onlyChildRun(t, fixture, result.SessionID)

	limit := childCompactionPageSize

	firstPage, err := fixture.runtime.ListSessionAgentRunMessages(
		ctx,
		result.SessionID,
		run.ID,
		api.ListSessionAgentRunMessagesParams{Limit: &limit},
	)
	require.NoError(t, err)
	require.Len(t, firstPage.Messages, int(childCompactionPageSize))
	assert.Equal(t, childCompactionPageSize, firstPage.Limit)
	assert.Equal(t, int32(0), firstPage.Offset)
	assert.True(t, firstPage.HasMore, "a bounded page reports more to read")

	offset := childCompactionPageSize

	secondPage, err := fixture.runtime.ListSessionAgentRunMessages(
		ctx,
		result.SessionID,
		run.ID,
		api.ListSessionAgentRunMessagesParams{
			Limit:  &limit,
			Offset: &offset,
		},
	)
	require.NoError(t, err)
	require.Len(t, secondPage.Messages, int(childCompactionPageSize))
	assert.Equal(t, childCompactionPageSize, secondPage.Offset)

	// Paging must neither overlap nor skip: the two pages are the child
	// transcript's first four rows, in transcript order.
	all, err := fixture.store.ListAgentRunMessages(
		ctx,
		result.SessionID,
		run.ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)
	require.Greater(t, len(all.Items), 2*int(childCompactionPageSize))

	paged := append(
		append([]api.AgentRunMessage{}, firstPage.Messages...),
		secondPage.Messages...,
	)
	for index, message := range paged {
		assert.Equal(t, all.Items[index].ID, message.Id)
		assert.Equal(t, run.ID, message.AgentRunId)
		assert.Equal(t, result.SessionID, message.SessionId)
	}

	compactions, err := fixture.runtime.ListSessionAgentRunCompactions(
		ctx,
		result.SessionID,
		run.ID,
		api.ListSessionAgentRunCompactionsParams{Limit: &limit},
	)
	require.NoError(t, err)
	require.NotEmpty(t, compactions.Compactions)
	assert.LessOrEqual(
		t,
		len(compactions.Compactions),
		int(childCompactionPageSize),
	)

	compactionID := compactions.Compactions[0].Id

	read, err := fixture.runtime.GetSessionAgentRunCompaction(
		ctx,
		result.SessionID,
		run.ID,
		compactionID,
	)
	require.NoError(t, err)
	assert.Equal(t, compactionID, read.Id)
	assert.Equal(t, run.ID, read.AgentRunId)
	assert.Equal(t, result.SessionID, read.SessionId)
	assert.NotEmpty(t, read.Summary)
	assert.Positive(t, read.SourceMessageCount)
	assert.Positive(t, read.SummaryTokenCount)
	assert.LessOrEqual(t, read.DirectFromSequence, read.DirectToSequence)

	// Another session owns none of this, so every route must refuse it.
	foreign := uuid.New()

	_, err = fixture.runtime.ListSessionAgentRunMessages(
		ctx,
		foreign,
		run.ID,
		api.ListSessionAgentRunMessagesParams{},
	)
	require.ErrorIs(t, err, commerr.ErrNotFound)

	_, err = fixture.runtime.ListSessionAgentRunCompactions(
		ctx,
		foreign,
		run.ID,
		api.ListSessionAgentRunCompactionsParams{},
	)
	require.ErrorIs(t, err, commerr.ErrNotFound)

	_, err = fixture.runtime.GetSessionAgentRunCompaction(
		ctx,
		foreign,
		run.ID,
		compactionID,
	)
	require.ErrorIs(t, err, commerr.ErrNotFound)

	// A run that belongs to no session at all is equally out of reach.
	_, err = fixture.runtime.ListSessionAgentRunMessages(
		ctx,
		result.SessionID,
		uuid.New(),
		api.ListSessionAgentRunMessagesParams{},
	)
	require.ErrorIs(t, err, commerr.ErrNotFound)

	// An out-of-range page size is a client error, not a silent clamp.
	overLimit := int32(session.MaximumPageLimit + 1)

	_, err = fixture.runtime.ListSessionAgentRunMessages(
		ctx,
		result.SessionID,
		run.ID,
		api.ListSessionAgentRunMessagesParams{Limit: &overLimit},
	)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

// A summarizer that fails must leave no compaction row behind. A half-written
// summary would claim messages it never covered, and replay would then skip
// them. The pre-compaction hook still runs, because the attempt was real; the
// post-compaction hook must not, because nothing completed.
func TestChildCompactionFailureLeavesNoRowAndNoPostHook(t *testing.T) {
	ctx := context.Background()

	// The child reaches its budget on the round after its last scripted tool
	// call, so the failing compaction is the last thing it does and the parent
	// reads its closing message next.
	driver := childCompactionDriver(t, childCompactionFailureRounds)
	fixture := childCompactionFixture(t, driver, childCompactionRepeatBudget)

	scriptRoot := t.TempDir()
	preInvocation := filepath.Join(scriptRoot, "pre.json")
	postInvocation := filepath.Join(scriptRoot, "post.json")
	writeChildCompactionHookScripts(
		t,
		fixture,
		scriptRoot,
		preInvocation,
		postInvocation,
	)

	attempts := 0
	fixture.runtime.compactionOptions.Summarize = func(
		_ context.Context,
		_ string,
	) (compactionSummary, error) {
		attempts++

		return compactionSummary{}, errChildCompactionSummarize
	}

	// A failing child compaction fails the child run, not the parent turn.
	result, err := fixture.runtime.Run(ctx, TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	require.Positive(t, attempts, "the child never attempted a compaction")

	query := repositories.Use(fixture.handle.GormDB)

	rows, err := query.AgentRunCompaction.WithContext(ctx).Find()
	require.NoError(t, err)
	assert.Empty(
		t,
		rows,
		"a failed child compaction must not leave a completed row",
	)

	run := onlyChildRun(t, fixture, result.SessionID)

	head, err := fixture.store.LatestAgentRunCompaction(
		ctx,
		result.SessionID,
		run.ID,
	)
	require.NoError(t, err)
	assert.Nil(t, head)

	// Persistence happens before the child loop continues, so the child's own
	// messages survive a compaction that never completed, and none of them is
	// claimed by a compaction that does not exist.
	messages, err := fixture.store.ListAgentRunMessages(
		ctx,
		result.SessionID,
		run.ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)
	require.NotEmpty(t, messages.Items)

	for _, message := range messages.Items {
		assert.Nil(t, message.CompactionID)
	}

	invocation := readCapturedInvocation(t, preInvocation)
	assert.Equal(t, result.SessionID, invocation.SessionID)
	assert.NotEqual(t, uuid.Nil, invocation.RequestID)
	assert.NotEqual(t, uuid.Nil, invocation.TurnID)
	assert.Positive(
		t,
		invocation.ContextTokens,
		"a compaction hook reports the context it was called about",
	)

	assert.NoFileExists(
		t,
		postInvocation,
		"a failed compaction must not report a completed one",
	)
}

// Both compaction lifecycle hooks fire for a child that does compact, under
// the child's own workspace and the launching turn's correlation.
func TestChildCompactionRunsBothLifecycleHooks(t *testing.T) {
	driver := childCompactionDriver(t, childCompactionRounds)
	fixture := childCompactionFixture(t, driver, childCompactionRepeatBudget)

	scriptRoot := t.TempDir()
	preInvocation := filepath.Join(scriptRoot, "pre.json")
	postInvocation := filepath.Join(scriptRoot, "post.json")
	writeChildCompactionHookScripts(
		t,
		fixture,
		scriptRoot,
		preInvocation,
		postInvocation,
	)

	summarizer := &childCompactionSummarizer{}
	fixture.runtime.compactionOptions.Summarize = summarizer.summarize

	result := runChildCompactionTurn(t, fixture)
	require.Positive(t, summarizer.count)
	run := onlyChildRun(t, fixture, result.SessionID)

	query := repositories.Use(fixture.handle.GormDB)

	turn, err := query.Turn.WithContext(context.Background()).
		Where(query.Turn.SessionID.Eq(result.SessionID)).
		First()
	require.NoError(t, err)

	for _, path := range []string{preInvocation, postInvocation} {
		invocation := readCapturedInvocation(t, path)

		assert.Equal(t, result.SessionID, invocation.SessionID)
		assert.Equal(
			t,
			turn.ID,
			invocation.TurnID,
			"a child compaction hook is correlated to the launching turn",
		)
		assert.Equal(t, fixture.workspace, invocation.Workspace)
		require.NotNil(t, invocation.AgentRunID)
		assert.Equal(t, run.ID, *invocation.AgentRunID)
	}
}

// writeChildCompactionHookScripts installs pre and post compaction command
// hooks that record the invocation JSON they read on stdin.
//
// Both the scripts and their output live in the test's own temporary
// directory, never in the workspace the agent can reach.
func writeChildCompactionHookScripts(
	t *testing.T,
	fixture runtimeFixture,
	root string,
	preOutput string,
	postOutput string,
) {
	t.Helper()

	writeChildHarnessHooks(t, fixture, `version: 1
pre_compact:
  - name: capture-pre
    actions:
      - type: command
        command: `+writeCaptureScript(t, root, "pre", preOutput)+`
post_compact:
  - name: capture-post
    actions:
      - type: command
        command: `+writeCaptureScript(t, root, "post", postOutput)+`
`)
}

func writeCaptureScript(
	t *testing.T,
	root string,
	name string,
	outputPath string,
) string {
	t.Helper()

	encodedPath, err := json.Marshal(outputPath)
	require.NoError(t, err)

	scriptPath := filepath.Join(root, name+"-capture.sh")
	require.NoError(t, os.WriteFile(
		scriptPath,
		[]byte("#!/bin/sh\nexec cat > "+string(encodedPath)+"\n"),
		childCompactionScriptMode,
	))

	return scriptPath
}

func readCapturedInvocation(t *testing.T, path string) hooks.Invocation {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the hook command never ran")

	var invocation hooks.Invocation
	require.NoError(t, json.Unmarshal(raw, &invocation))

	return invocation
}
