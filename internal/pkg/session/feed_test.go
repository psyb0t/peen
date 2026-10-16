package session

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const feedTestNoticeType = "app.ready"

// feedChildFixture creates a native child agent run and its execution with a
// matching worker generation, so an activity claimed under generationID owns
// both.
func feedChildFixture(
	t *testing.T,
	fixture executionFixture,
	generationID uuid.UUID,
) (*models.AgentRun, *models.Execution) {
	t.Helper()

	run, err := fixture.store.CreateAgentRun(t.Context(), fixture.sessionID,
		StartAgentRunInput{
			ID: uuid.New(), ParentTurnID: fixture.turnID,
			RequestID: uuid.New(), Name: "feedChild",
			Definition: models.AgentRunDefinitionAdHoc, Depth: 1,
			Workspace: testWorkspace, ModelReference: agentRunTestModel,
			ModelID: agentRunTestModel, Task: executionTestTask,
			Instructions: agentRunTestInstructions, AllowedToolsJSON: `[]`,
			SystemPrompt:       agentRunTestPrompt,
			WorkerGenerationID: generationID.String(),
		})
	require.NoError(t, err)

	input := executionTestInput(fixture.turnID)
	input.AgentRunID = &run.ID
	input.ParentExecutionID = &fixture.root.ID
	child, err := fixture.store.CreateExecution(t.Context(),
		fixture.sessionID, input)
	require.NoError(t, err)

	return run, child
}

func feedChildActivityInput() ScheduleActivityInput {
	return ScheduleActivityInput{
		StepID: "feedStep", Name: "feedActivity",
		Kind:         models.ActivityKindTool,
		ReplayPolicy: models.ActivityReplayPolicyRetry,
		Input:        json.RawMessage(`{}`),
	}
}

func assertFeedContiguous(t *testing.T, sequences []int64, start int64) {
	t.Helper()

	seen := make(map[int64]struct{}, len(sequences))
	for _, sequence := range sequences {
		_, duplicate := seen[sequence]
		assert.False(t, duplicate, "duplicate feed sequence %d", sequence)
		seen[sequence] = struct{}{}
	}

	for index, sequence := range sequences {
		assert.Equal(t, start+int64(index), sequence)
	}
}

func TestFeedSequenceContiguousAcrossMixedWriters(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	store := fixture.store

	lease := inboxTestLease(t, fixture)
	require.NoError(t, store.AppendCheckpoint(t.Context(), lease, nil,
		[]EventInput{{EventType: "turn.checkpoint", PayloadJSON: `{}`}}))

	generationID := uuid.New()
	run, child := feedChildFixture(t, fixture, generationID)

	_, err := store.AppendAgentRunEvent(t.Context(), fixture.sessionID,
		run.ID, AgentRunEventInput{
			EventType: "agent.run.text.delta", PayloadJSON: `{"text":"a"}`,
		})
	require.NoError(t, err)

	claim, err := store.BeginExecutionActivity(t.Context(), fixture.sessionID,
		child.ID, generationID, feedChildActivityInput())
	require.NoError(t, err)

	_, err = store.CompleteExecutionActivityReceipt(t.Context(),
		fixture.sessionID, claim.Lease, CompleteActivityInput{
			Output: json.RawMessage(`{}`), Context: json.RawMessage(`{}`),
			ChildProjection: &ExecutionChildProjection{
				ControlRevision: child.ControlRevision,
				Events: []AgentRunEventInput{
					{
						ID:          uuid.New(),
						EventType:   "agent.run.text.delta",
						PayloadJSON: `{"text":"b"}`,
					},
					{
						ID:          uuid.New(),
						EventType:   "agent.run.text.delta",
						PayloadJSON: `{"text":"c"}`,
					},
				},
			},
		})
	require.NoError(t, err)

	_, _, err = store.RequestAgentRunCancellation(t.Context(),
		fixture.sessionID, run.ID)
	require.NoError(t, err)

	_, err = store.CreateSessionNotice(t.Context(), fixture.sessionID,
		CreateSessionNoticeInput{
			Type: feedTestNoticeType, Source: "example", Summary: "ready",
			DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
		})
	require.NoError(t, err)

	page, err := store.ListFeed(t.Context(), fixture.sessionID, 0, 200)
	require.NoError(t, err)
	assert.False(t, page.HasMore)
	require.Len(t, page.Items, 6)

	sequences := make([]int64, 0, len(page.Items))
	kinds := make([]FeedKind, 0, len(page.Items))

	for _, item := range page.Items {
		sequences = append(sequences, item.Sequence)
		kinds = append(kinds, item.Kind)
	}

	assertFeedContiguous(t, sequences, 1)
	assert.Equal(t, []FeedKind{
		FeedKindEvent, FeedKindAgentRunEvent, FeedKindAgentRunEvent,
		FeedKindAgentRunEvent, FeedKindAgentRunEvent, FeedKindNotice,
	}, kinds)

	head, err := store.FeedHead(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, int64(6), head)
	assert.Equal(t, head, page.Head)
}

func TestFeedSequenceCountsPerSessionIndependently(t *testing.T) {
	t.Parallel()

	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	first := newTestSession(t.Context(), t, store)
	second := newTestSession(t.Context(), t, store)

	_, err := store.CreateSessionNotice(t.Context(), first.ID,
		CreateSessionNoticeInput{
			Type: feedTestNoticeType, Source: "example", Summary: "ready",
			DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
		})
	require.NoError(t, err)
	_, err = store.CreateSessionNotice(t.Context(), first.ID,
		CreateSessionNoticeInput{
			Type: feedTestNoticeType, Source: "example", Summary: "ready",
			DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
		})
	require.NoError(t, err)

	_, err = store.CreateSessionNotice(t.Context(), second.ID,
		CreateSessionNoticeInput{
			Type: feedTestNoticeType, Source: "example", Summary: "ready",
			DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
		})
	require.NoError(t, err)

	firstHead, err := store.FeedHead(t.Context(), first.ID)
	require.NoError(t, err)
	secondHead, err := store.FeedHead(t.Context(), second.ID)
	require.NoError(t, err)

	assert.Equal(t, int64(2), firstHead)
	assert.Equal(t, int64(1), secondHead)
}

func TestFeedSequenceContiguousUnderConcurrentWriters(t *testing.T) {
	t.Parallel()

	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	session := newTestSession(t.Context(), t, store)

	const writerCount = 20

	start := make(chan struct{})

	var waitGroup sync.WaitGroup
	for range writerCount {
		waitGroup.Go(func() {
			<-start

			_, err := store.CreateSessionNotice(t.Context(), session.ID,
				CreateSessionNoticeInput{
					Type: feedTestNoticeType, Source: "example",
					Summary: "ready", DataJSON: `{}`,
					Delivery: models.NoticeDeliveryQueue,
				})
			assert.NoError(t, err)
		})
	}

	close(start)
	waitGroup.Wait()

	page, err := store.ListFeed(t.Context(), session.ID, 0, 200)
	require.NoError(t, err)
	require.Len(t, page.Items, writerCount)

	sequences := make([]int64, 0, len(page.Items))
	for _, item := range page.Items {
		sequences = append(sequences, item.Sequence)
	}

	assertFeedContiguous(t, sequences, 1)
}

// feedSignalCall is one recorded SetFeedSignal invocation.
type feedSignalCall struct {
	sessionID uuid.UUID
	first     int64
}

// feedSignalRecorder collects SetFeedSignal calls for assertion, safe for
// concurrent use because writers may fire it from different goroutines.
type feedSignalRecorder struct {
	mu    sync.Mutex
	calls []feedSignalCall
}

func (r *feedSignalRecorder) signal(sessionID uuid.UUID, first int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, feedSignalCall{sessionID: sessionID, first: first})
}

func (r *feedSignalRecorder) snapshot() []feedSignalCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]feedSignalCall, len(r.calls))
	copy(out, r.calls)

	return out
}

// feedWriteResult is what one table case reports after performing its
// exported write call, so the shared assertion can check the signal and the
// resulting head regardless of how much setup the case needed first.
type feedWriteResult struct {
	store      *Store
	sessionID  uuid.UUID
	headBefore int64
	rowCount   int64
}

func TestFeedSignalFiresAfterCommitForEveryWritePath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		run  func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult
	}{
		{
			name: "AppendCheckpoint",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				fixture := newExecutionFixture(t)
				fixture.store.SetFeedSignal(recorder.signal)
				lease := inboxTestLease(t, fixture)

				headBefore, err := fixture.store.FeedHead(
					t.Context(), fixture.sessionID)
				require.NoError(t, err)

				require.NoError(t, fixture.store.AppendCheckpoint(
					t.Context(), lease, nil, []EventInput{
						{EventType: "turn.checkpoint", PayloadJSON: `{}`},
					}))

				return feedWriteResult{
					store: fixture.store, sessionID: fixture.sessionID,
					headBefore: headBefore, rowCount: 1,
				}
			},
		},
		{
			name: "FinalizeTurn",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				fixture := newExecutionFixture(t)
				fixture.store.SetFeedSignal(recorder.signal)
				lease := inboxTestLease(t, fixture)

				headBefore, err := fixture.store.FeedHead(
					t.Context(), fixture.sessionID)
				require.NoError(t, err)

				require.NoError(t, fixture.store.FinalizeTurn(
					t.Context(), lease, FinalizeTurnInput{
						State: models.TurnStateCompleted,
						Events: []EventInput{
							{EventType: "turn.completed", PayloadJSON: `{}`},
						},
					}))

				return feedWriteResult{
					store: fixture.store, sessionID: fixture.sessionID,
					headBefore: headBefore, rowCount: 1,
				}
			},
		},
		{
			name: "AcquireTurn",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				store, handle := openTestStore(t)
				t.Cleanup(func() { require.NoError(t, handle.Close()) })
				store.SetFeedSignal(recorder.signal)
				session := newTestSession(t.Context(), t, store)

				_, err := store.AcquireTurn(t.Context(), session.ID,
					StartTurnInput{
						RequestID: uuid.New(), Workspace: testWorkspace,
						Events: []EventInput{
							{EventType: "turn.started", PayloadJSON: `{}`},
						},
					})
				require.NoError(t, err)

				return feedWriteResult{
					store: store, sessionID: session.ID,
					headBefore: 0, rowCount: 1,
				}
			},
		},
		{
			name: "CreateSessionNotice",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				store, handle := openTestStore(t)
				t.Cleanup(func() { require.NoError(t, handle.Close()) })
				store.SetFeedSignal(recorder.signal)
				session := newTestSession(t.Context(), t, store)

				_, err := store.CreateSessionNotice(t.Context(), session.ID,
					CreateSessionNoticeInput{
						Type: feedTestNoticeType, Source: "example",
						Summary: "ready", DataJSON: `{}`,
						Delivery: models.NoticeDeliveryQueue,
					})
				require.NoError(t, err)

				return feedWriteResult{
					store: store, sessionID: session.ID,
					headBefore: 0, rowCount: 1,
				}
			},
		},
		{
			name: "AppendAgentRunEvent",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				fixture := newExecutionFixture(t)
				fixture.store.SetFeedSignal(recorder.signal)
				run := createAgentRunForTest(t.Context(), t, fixture.store,
					fixture.sessionID, fixture.turnID, 0)

				headBefore, err := fixture.store.FeedHead(
					t.Context(), fixture.sessionID)
				require.NoError(t, err)

				_, err = fixture.store.AppendAgentRunEvent(t.Context(),
					fixture.sessionID, run.ID, AgentRunEventInput{
						EventType:   "agent.run.text.delta",
						PayloadJSON: `{"text":"a"}`,
					})
				require.NoError(t, err)

				return feedWriteResult{
					store: fixture.store, sessionID: fixture.sessionID,
					headBefore: headBefore, rowCount: 1,
				}
			},
		},
		{
			name: "RequestAgentRunCancellation",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				fixture := newExecutionFixture(t)
				fixture.store.SetFeedSignal(recorder.signal)
				generationID := uuid.New()
				run, _ := feedChildFixture(t, fixture, generationID)

				headBefore, err := fixture.store.FeedHead(
					t.Context(), fixture.sessionID)
				require.NoError(t, err)

				_, _, err = fixture.store.RequestAgentRunCancellation(
					t.Context(), fixture.sessionID, run.ID)
				require.NoError(t, err)

				return feedWriteResult{
					store: fixture.store, sessionID: fixture.sessionID,
					headBefore: headBefore, rowCount: 1,
				}
			},
		},
		{
			name: "CompleteExecutionActivityReceipt",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				fixture := newExecutionFixture(t)
				fixture.store.SetFeedSignal(recorder.signal)
				generationID := uuid.New()
				_, child := feedChildFixture(t, fixture, generationID)
				claim, err := fixture.store.BeginExecutionActivity(
					t.Context(), fixture.sessionID, child.ID, generationID,
					feedChildActivityInput())
				require.NoError(t, err)

				headBefore, err := fixture.store.FeedHead(
					t.Context(), fixture.sessionID)
				require.NoError(t, err)

				_, err = fixture.store.CompleteExecutionActivityReceipt(
					t.Context(), fixture.sessionID, claim.Lease,
					CompleteActivityInput{
						Output:  json.RawMessage(`{}`),
						Context: json.RawMessage(`{}`),
						ChildProjection: &ExecutionChildProjection{
							ControlRevision: child.ControlRevision,
							Events: []AgentRunEventInput{
								{
									ID:          uuid.New(),
									EventType:   "agent.run.text.delta",
									PayloadJSON: `{"text":"a"}`,
								},
								{
									ID:          uuid.New(),
									EventType:   "agent.run.text.delta",
									PayloadJSON: `{"text":"b"}`,
								},
							},
						},
					})
				require.NoError(t, err)

				return feedWriteResult{
					store: fixture.store, sessionID: fixture.sessionID,
					headBefore: headBefore, rowCount: 2,
				}
			},
		},
		{
			name: "AcquireDeferredExecutionTurn",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				fixture, ids := deferredReadFixture(t, 1)
				fixture.store.SetFeedSignal(recorder.signal)
				fixture.store.ReleaseTurn(deferredTestLease(t, fixture))
				page, err := fixture.store.ListDeferredExecutionMessages(
					t.Context(), fixture.sessionID, ListEventsOptions{})
				require.NoError(t, err)
				require.Len(t, page.Items, 1)
				message := page.Items[0]

				headBefore, err := fixture.store.FeedHead(
					t.Context(), fixture.sessionID)
				require.NoError(t, err)

				_, err = fixture.store.AcquireDeferredExecutionTurn(
					t.Context(), fixture.sessionID, ids[0], StartTurnInput{
						RequestID: message.RequestID,
						Workspace: message.WorkingDirectory,
						Events: []EventInput{
							{EventType: "turn.started", PayloadJSON: `{}`},
						},
					})
				require.NoError(t, err)

				return feedWriteResult{
					store: fixture.store, sessionID: fixture.sessionID,
					headBefore: headBefore, rowCount: 1,
				}
			},
		},
		{
			name: "DeliverExecutionMessages",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				fixture := newExecutionFixture(t)
				fixture.store.SetFeedSignal(recorder.signal)
				lease := inboxTestLease(t, fixture)
				accepted := inboxTestInput()
				_, err := fixture.store.AcceptExecutionMessage(t.Context(),
					fixture.root.ID, lease, accepted)
				require.NoError(t, err)
				requestIDs := []uuid.UUID{accepted.RequestID}
				claim := inboxTestClaim(t, fixture, requestIDs)

				headBefore, err := fixture.store.FeedHead(
					t.Context(), fixture.sessionID)
				require.NoError(t, err)

				delivery, err := fixture.store.DeliverExecutionMessages(
					t.Context(), fixture.sessionID,
					DeliverExecutionMessagesInput{
						TurnLease: lease, ModelLease: claim.Lease,
						RequestIDs: requestIDs,
					})
				require.NoError(t, err)

				return feedWriteResult{
					store: fixture.store, sessionID: fixture.sessionID,
					headBefore: headBefore,
					rowCount:   int64(len(delivery.Events)),
				}
			},
		},
		{
			name: "AcceptExecutionMessage",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				fixture := newExecutionFixture(t)
				fixture.store.SetFeedSignal(recorder.signal)
				lease := inboxTestLease(t, fixture)

				headBefore, err := fixture.store.FeedHead(
					t.Context(), fixture.sessionID)
				require.NoError(t, err)

				accepted, err := fixture.store.AcceptExecutionMessage(
					t.Context(), fixture.root.ID, lease, inboxTestInput())
				require.NoError(t, err)

				return feedWriteResult{
					store: fixture.store, sessionID: fixture.sessionID,
					headBefore: headBefore,
					rowCount:   int64(len(accepted.Events)),
				}
			},
		},
		{
			name: "DeferExecutionMessage",
			run: func(t *testing.T, recorder *feedSignalRecorder) feedWriteResult {
				t.Helper()

				fixture, _ := sealedCompletionFixture(t)
				fixture.store.SetFeedSignal(recorder.signal)
				lease := deferredTestLease(t, fixture)

				headBefore, err := fixture.store.FeedHead(
					t.Context(), fixture.sessionID)
				require.NoError(t, err)

				deferred, err := fixture.store.DeferExecutionMessage(
					t.Context(), fixture.root.ID, lease, inboxTestInput())
				require.NoError(t, err)

				return feedWriteResult{
					store: fixture.store, sessionID: fixture.sessionID,
					headBefore: headBefore,
					rowCount:   int64(len(deferred.Events)),
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := &feedSignalRecorder{}
			result := tc.run(t, recorder)

			calls := recorder.snapshot()
			require.NotEmpty(t, calls, "write path never signalled")

			last := calls[len(calls)-1]
			assert.Equal(t, result.sessionID, last.sessionID)
			assert.Equal(t, result.headBefore+1, last.first)

			headAfter, err := result.store.FeedHead(
				t.Context(), result.sessionID)
			require.NoError(t, err)
			assert.Equal(t, result.headBefore+result.rowCount, headAfter)
		})
	}
}

// TestFeedSignalNeverFiresOnRollback forces a validation failure inside the
// write transaction, after the feed sequence allocation would have run, and
// asserts the signal never fires and the head never advances.
func TestFeedSignalNeverFiresOnRollback(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	recorder := &feedSignalRecorder{}
	fixture.store.SetFeedSignal(recorder.signal)

	headBefore, err := fixture.store.FeedHead(t.Context(), fixture.sessionID)
	require.NoError(t, err)

	lease := inboxTestLease(t, fixture)
	err = fixture.store.AppendCheckpoint(t.Context(), lease, nil,
		[]EventInput{{EventType: "turn.checkpoint", PayloadJSON: "not json"}})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)

	assert.Empty(t, recorder.snapshot())

	headAfter, err := fixture.store.FeedHead(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, headBefore, headAfter)
}

// TestFeedAllocationRequiresFeedTransaction proves a write path that skips
// feedTransaction fails instead of committing rows nobody signals.
func TestFeedAllocationRequiresFeedTransaction(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)

	headBefore, err := fixture.store.FeedHead(t.Context(), fixture.sessionID)
	require.NoError(t, err)

	_, err = fixture.store.allocateFeedSequences(t.Context(),
		fixture.store.query, fixture.sessionID, 1)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)

	headAfter, err := fixture.store.FeedHead(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, headBefore, headAfter)
}

func TestListFeedValidatesCursorAndLimit(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)

	testCases := []struct {
		name   string
		cursor int64
		limit  int
	}{
		{name: "negative cursor", cursor: -1, limit: 10},
		{name: "zero limit", cursor: 0, limit: 0},
		{name: "limit over maximum", cursor: 0, limit: MaximumFeedLimit + 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := fixture.store.ListFeed(t.Context(), fixture.sessionID,
				tc.cursor, tc.limit)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestListFeedUnknownSessionIsNotFound(t *testing.T) {
	t.Parallel()

	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	_, err := store.ListFeed(t.Context(), uuid.New(), 0, 10)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

// TestListFeedMergesPagesAndStopsAtHead writes an event checkpoint, an
// agent run event, and a notice on one session, then walks the feed two
// items at a time, checking merge order, HasMore, NextCursor, and the empty
// page a cursor at the head returns.
func TestListFeedMergesPagesAndStopsAtHead(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	store := fixture.store

	lease := inboxTestLease(t, fixture)
	require.NoError(t, store.AppendCheckpoint(t.Context(), lease, nil,
		[]EventInput{
			{EventType: "turn.checkpoint", PayloadJSON: `{"n":1}`},
			{EventType: "turn.checkpoint", PayloadJSON: `{"n":2}`},
		}))

	generationID := uuid.New()
	run, _ := feedChildFixture(t, fixture, generationID)
	_, err := store.AppendAgentRunEvent(t.Context(), fixture.sessionID,
		run.ID, AgentRunEventInput{
			EventType: "agent.run.text.delta", PayloadJSON: `{"text":"a"}`,
		})
	require.NoError(t, err)

	_, err = store.CreateSessionNotice(t.Context(), fixture.sessionID,
		CreateSessionNoticeInput{
			Type: feedTestNoticeType, Source: "example", Summary: "ready",
			DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
		})
	require.NoError(t, err)

	head, err := store.FeedHead(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	require.Equal(t, int64(4), head)

	first, err := store.ListFeed(t.Context(), fixture.sessionID, 0, 2)
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	assert.True(t, first.HasMore)
	assert.Equal(t, int64(2), first.NextCursor)
	assert.Equal(t, head, first.Head)
	assert.Equal(t, FeedKindEvent, first.Items[0].Kind)
	assert.Equal(t, FeedKindEvent, first.Items[1].Kind)
	assert.Equal(t, fixture.turnID, first.Items[0].TurnID)

	second, err := store.ListFeed(t.Context(), fixture.sessionID,
		first.NextCursor, 2)
	require.NoError(t, err)
	require.Len(t, second.Items, 2)
	assert.False(t, second.HasMore)
	assert.Equal(t, int64(4), second.NextCursor)
	assert.Equal(t, FeedKindAgentRunEvent, second.Items[0].Kind)
	assert.Equal(t, FeedKindNotice, second.Items[1].Kind)
	assert.Equal(t, run.ParentTurnID, second.Items[0].ParentTurnID)
	assert.Nil(t, second.Items[0].ParentAgentRunID)
	assert.Equal(t, run.ID, second.Items[0].AgentRunID)
	require.NotNil(t, second.Items[1].Notice)
	assert.Equal(t, feedTestNoticeType, second.Items[1].Notice.Type)
	assert.Equal(t, FeedEventTypeNoticeCreated, second.Items[1].EventType)

	beyond, err := store.ListFeed(t.Context(), fixture.sessionID, head, 2)
	require.NoError(t, err)
	assert.Empty(t, beyond.Items)
	assert.False(t, beyond.HasMore)
	assert.Equal(t, head, beyond.NextCursor)
	assert.Equal(t, head, beyond.Head)
}

func TestFeedHeadUnknownSessionIsNotFound(t *testing.T) {
	t.Parallel()

	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	_, err := store.FeedHead(t.Context(), uuid.New())
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestTurnFeedStartWithAndWithoutEvents(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	store := fixture.store

	_, found, err := store.TurnFeedStart(t.Context(), fixture.sessionID,
		fixture.turnID)
	require.NoError(t, err)
	assert.False(t, found)

	lease := inboxTestLease(t, fixture)
	require.NoError(t, store.AppendCheckpoint(t.Context(), lease, nil,
		[]EventInput{
			{EventType: "turn.checkpoint", PayloadJSON: `{"n":1}`},
			{EventType: "turn.checkpoint", PayloadJSON: `{"n":2}`},
		}))

	start, found, err := store.TurnFeedStart(t.Context(), fixture.sessionID,
		fixture.turnID)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(1), start)
}

func TestTurnFeedStartUnknownSessionIsNotFound(t *testing.T) {
	t.Parallel()

	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	_, _, err := store.TurnFeedStart(t.Context(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, commerr.ErrNotFound)
}
