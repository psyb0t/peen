package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAgentRunRegistryValidatesOptions(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		sessionID uuid.UUID
		limits    AgentRunLimits
		wantErr   error
	}{
		{
			name:      "nil session is rejected",
			sessionID: uuid.Nil,
			wantErr:   ErrInvalidAgentRunOptions,
		},
		{
			name:      "zero limits take defaults",
			sessionID: uuid.New(),
		},
		{
			name:      "negative limit is rejected",
			sessionID: uuid.New(),
			limits:    AgentRunLimits{MaxDepth: -1},
			wantErr:   ErrInvalidAgentRunLimits,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			registry, err := NewAgentRunRegistry(tc.sessionID, nil, tc.limits)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, registry)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, registry)
		})
	}
}

type agentRunTestKey struct{}

// A child outlives the tool call that launched it: the run's context keeps
// the caller's values but not its cancellation.
func TestAgentRunRegistryStartContextOutlivesTheCaller(t *testing.T) {
	t.Parallel()

	registry, err := NewAgentRunRegistry(uuid.New(), nil, AgentRunLimits{})
	require.NoError(t, err)

	parentCtx, cancel := context.WithCancel(
		context.WithValue(context.Background(), agentRunTestKey{}, "kept"),
	)
	run, runCtx, err := registry.Start(parentCtx, StartAgentRunInput{
		Name:       "child",
		Definition: AgentRunDefinitionStored,
		Depth:      1,
	})
	require.NoError(t, err)
	assert.Equal(t, AgentRunStateRunning, run.Snapshot().State)

	cancel()
	require.NoError(t, runCtx.Err())
	assert.Equal(t, "kept", runCtx.Value(agentRunTestKey{}))

	registry.Cancel(run.ID)
	assert.ErrorIs(t, runCtx.Err(), context.Canceled)
}

func TestAgentRunRegistryCancelTurnStopsOnlyThatTurnsRuns(t *testing.T) {
	t.Parallel()

	registry, err := NewAgentRunRegistry(uuid.New(), nil, AgentRunLimits{})
	require.NoError(t, err)

	cancelledTurn, otherTurn := uuid.New(), uuid.New()

	child, childCtx, err := registry.Start(
		context.Background(),
		StartAgentRunInput{
			Name:         "child",
			ParentTurnID: cancelledTurn,
			Depth:        1,
		},
	)
	require.NoError(t, err)

	_, grandchildCtx, err := registry.Start(
		context.Background(),
		StartAgentRunInput{
			Name:             "grandchild",
			ParentTurnID:     cancelledTurn,
			ParentAgentRunID: &child.ID,
			Depth:            2,
		},
	)
	require.NoError(t, err)

	_, otherCtx, err := registry.Start(
		context.Background(),
		StartAgentRunInput{Name: "other", ParentTurnID: otherTurn, Depth: 1},
	)
	require.NoError(t, err)

	assert.Equal(t, 2, registry.CancelTurn(cancelledTurn))
	require.ErrorIs(t, childCtx.Err(), context.Canceled)
	require.ErrorIs(t, grandchildCtx.Err(), context.Canceled)
	require.NoError(t, otherCtx.Err())

	assert.Equal(t, 1, registry.CancelRunning())
	assert.ErrorIs(t, otherCtx.Err(), context.Canceled)
}

func TestAgentRunRegistryCancelDescendantsLeavesTheParentAndSiblings(
	t *testing.T,
) {
	t.Parallel()

	registry, err := NewAgentRunRegistry(uuid.New(), nil, AgentRunLimits{
		MaxConcurrentRuns: 8,
	})
	require.NoError(t, err)

	start := func(name string, parent *uuid.UUID) (*AgentRun, context.Context) {
		t.Helper()

		run, runCtx, err := registry.Start(
			context.Background(),
			StartAgentRunInput{Name: name, ParentAgentRunID: parent},
		)
		require.NoError(t, err)

		return run, runCtx
	}

	parent, parentCtx := start("parent", nil)
	sibling, siblingCtx := start("sibling", nil)
	child, childCtx := start("child", &parent.ID)
	_, grandchildCtx := start("grandchild", &child.ID)
	_, cousinCtx := start("cousin", &sibling.ID)

	assert.Equal(t, 2, registry.cancelDescendants(parent.ID))
	require.ErrorIs(t, childCtx.Err(), context.Canceled)
	require.ErrorIs(t, grandchildCtx.Err(), context.Canceled)
	require.NoError(t, parentCtx.Err())
	require.NoError(t, siblingCtx.Err())
	assert.NoError(t, cousinCtx.Err())
}

func TestAgentRunRegistryDeliverReportsUnknownAndFinishedRuns(t *testing.T) {
	t.Parallel()

	registry, err := NewAgentRunRegistry(uuid.New(), nil, AgentRunLimits{})
	require.NoError(t, err)

	err = registry.Deliver(context.Background(), uuid.New(), "hello")
	require.ErrorIs(t, err, commerr.ErrNotFound)

	run, _, err := registry.Start(context.Background(), StartAgentRunInput{
		Name: "child",
	})
	require.NoError(t, err)

	err = registry.Deliver(context.Background(), run.ID, "no inbox")
	require.ErrorIs(t, err, session.ErrAgentRunFinished)

	registry.Finish(
		context.Background(),
		run,
		AgentRunResult{State: AgentRunStateCompleted},
	)

	err = registry.Deliver(context.Background(), run.ID, "too late")
	assert.ErrorIs(t, err, session.ErrAgentRunFinished)
}

func TestAgentRunRegistryWaitReturnsWhenEveryRunEnds(t *testing.T) {
	t.Parallel()

	registry, err := NewAgentRunRegistry(uuid.New(), nil, AgentRunLimits{})
	require.NoError(t, err)

	run, _, err := registry.Start(context.Background(), StartAgentRunInput{
		Name: "child",
	})
	require.NoError(t, err)

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, registry.Wait(expired), context.Canceled)

	registry.Finish(
		context.Background(),
		run,
		AgentRunResult{State: AgentRunStateCancelled},
	)
	assert.NoError(t, registry.Wait(context.Background()))
}

func TestAgentRunRegistryStartEnforcesConcurrencyLimit(t *testing.T) {
	t.Parallel()

	registry, err := NewAgentRunRegistry(
		uuid.New(),
		nil,
		AgentRunLimits{MaxConcurrentRuns: 2},
	)
	require.NoError(t, err)

	first, _, err := registry.Start(context.Background(), StartAgentRunInput{
		Name: "a", Definition: AgentRunDefinitionStored, Depth: 1,
	})
	require.NoError(t, err)

	_, _, err = registry.Start(context.Background(), StartAgentRunInput{
		Name: "b", Definition: AgentRunDefinitionStored, Depth: 1,
	})
	require.NoError(t, err)

	_, _, err = registry.Start(context.Background(), StartAgentRunInput{
		Name: "c", Definition: AgentRunDefinitionStored, Depth: 1,
	})
	require.ErrorIs(t, err, ErrTooManyAgentRuns)

	// Freeing a slot by finishing a run lets a new one start again.
	registry.Finish(
		context.Background(),
		first,
		AgentRunResult{State: AgentRunStateCompleted},
	)

	third, _, err := registry.Start(context.Background(), StartAgentRunInput{
		Name: "c", Definition: AgentRunDefinitionStored, Depth: 1,
	})
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, third.ID)
}

func TestAgentRunRegistryCancelIsIdempotent(t *testing.T) {
	t.Parallel()

	registry, err := NewAgentRunRegistry(uuid.New(), nil, AgentRunLimits{})
	require.NoError(t, err)

	_, found := registry.Cancel(uuid.New())
	assert.False(t, found, "an unknown run id reports not found, not an error")

	run, runCtx, err := registry.Start(context.Background(), StartAgentRunInput{
		Name: "child", Definition: AgentRunDefinitionStored, Depth: 1,
	})
	require.NoError(t, err)

	snapshot, found := registry.Cancel(run.ID)
	require.True(t, found)
	assert.Error(t, runCtx.Err(), "cancel must signal the run's own context")
	assert.Equal(
		t,
		AgentRunStateRunning,
		snapshot.State,
		"cancel only signals; the state turns terminal once Finish runs",
	)

	// Cancelling again before Finish is still idempotent.
	snapshot, found = registry.Cancel(run.ID)
	require.True(t, found)
	assert.Equal(t, AgentRunStateRunning, snapshot.State)

	registry.Finish(
		context.Background(),
		run,
		AgentRunResult{State: AgentRunStateCancelled},
	)

	// Cancelling an already-completed run reports its current state rather
	// than failing.
	snapshot, found = registry.Cancel(run.ID)
	require.True(t, found)
	assert.Equal(t, AgentRunStateCancelled, snapshot.State)
}

func TestAgentRunRegistryCancelUnknownAcrossSessions(t *testing.T) {
	t.Parallel()

	registryA, err := NewAgentRunRegistry(uuid.New(), nil, AgentRunLimits{})
	require.NoError(t, err)
	registryB, err := NewAgentRunRegistry(uuid.New(), nil, AgentRunLimits{})
	require.NoError(t, err)

	run, _, err := registryA.Start(context.Background(), StartAgentRunInput{
		Name: "child", Definition: AgentRunDefinitionStored, Depth: 1,
	})
	require.NoError(t, err)

	_, foundInA := registryA.Get(run.ID)
	assert.True(t, foundInA)

	_, foundInB := registryB.Get(run.ID)
	assert.False(
		t,
		foundInB,
		"a run id from another session must not be found",
	)

	_, cancelledInB := registryB.Cancel(run.ID)
	assert.False(t, cancelledInB)
}

// Late cleanup of a finished run must never disturb a newer, unrelated run:
// registry state is keyed by the run's own unique ID, so completing an old
// run cannot reach a run started afterward.
func TestAgentRunRegistryLateCleanupNeverTouchesNewerRun(t *testing.T) {
	t.Parallel()

	registry, err := NewAgentRunRegistry(uuid.New(), nil, AgentRunLimits{})
	require.NoError(t, err)

	older, _, err := registry.Start(context.Background(), StartAgentRunInput{
		Name: "older", Definition: AgentRunDefinitionStored, Depth: 1,
	})
	require.NoError(t, err)

	newer, _, err := registry.Start(context.Background(), StartAgentRunInput{
		Name: "newer", Definition: AgentRunDefinitionStored, Depth: 1,
	})
	require.NoError(t, err)

	// The older run finishes late, after the newer one has already started.
	registry.Finish(
		context.Background(),
		older,
		AgentRunResult{State: AgentRunStateCompleted},
	)

	assert.Equal(t, AgentRunStateCompleted, older.Snapshot().State)
	assert.Equal(
		t,
		AgentRunStateRunning,
		newer.Snapshot().State,
		"a late finish on an older run must not affect a newer one",
	)
}

func TestAgentRunRegistryFinishPublishesCompletionEvents(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	bus := events.NewBus(events.Options{})
	registry, err := NewAgentRunRegistry(sessionID, bus, AgentRunLimits{})
	require.NoError(t, err)

	testCases := []struct {
		name         string
		result       AgentRunResult
		wantEvent    events.Type
		wantDelivery events.Delivery
	}{
		{
			"completed",
			AgentRunResult{State: AgentRunStateCompleted, Response: "done"},
			events.TypeAgentFinished,
			events.DeliveryWake,
		},
		{
			"failed",
			AgentRunResult{State: AgentRunStateFailed, FailureDetail: "boom"},
			events.TypeAgentFailed,
			events.DeliveryWake,
		},
		{
			"cancelled",
			AgentRunResult{State: AgentRunStateCancelled},
			events.TypeAgentFailed,
			events.DeliveryQueue,
		},
	}

	for _, tc := range testCases {
		run, _, err := registry.Start(context.Background(), StartAgentRunInput{
			Name: tc.name, Definition: AgentRunDefinitionStored, Depth: 1,
		})
		require.NoError(t, err)

		registry.Finish(context.Background(), run, tc.result)

		batch := bus.Drain(sessionID)
		require.Len(t, batch.Notices, 1)
		assert.Equal(t, tc.wantEvent, batch.Notices[0].Type)
		assert.Equal(t, tc.wantDelivery, batch.Notices[0].Delivery)

		var data agentEventData
		require.NoError(t, json.Unmarshal(batch.Notices[0].Data, &data))
		assert.Equal(t, run.ID, data.RunID)
		assert.Equal(t, tc.name, data.AgentName)
		assert.Equal(t, tc.result.State, data.State)
		assert.Equal(t, tc.result.Response, data.Response)
		assert.Equal(t, tc.result.FailureDetail, data.FailureDetail)
		assert.False(t, data.ResponseTruncated)
		assert.GreaterOrEqual(t, data.DurationMs, int64(0))
		assert.NotEmpty(t, data.Duration)

		raw := map[string]any{}
		require.NoError(t, json.Unmarshal(batch.Notices[0].Data, &raw))
		assert.Contains(t, raw, "durationMs")
		assert.Contains(t, raw, "duration")
	}
}

// A nested child reports to its parent run's inbox, not to the root, so its
// end publishes no session event.
func TestAgentRunRegistryFinishOfANestedRunPublishesNothing(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	bus := events.NewBus(events.Options{})
	registry, err := NewAgentRunRegistry(sessionID, bus, AgentRunLimits{})
	require.NoError(t, err)

	parentID := uuid.New()
	run, _, err := registry.Start(context.Background(), StartAgentRunInput{
		Name:             "nested",
		ParentAgentRunID: &parentID,
		Depth:            2,
	})
	require.NoError(t, err)

	registry.Finish(
		context.Background(),
		run,
		AgentRunResult{State: AgentRunStateCompleted, Response: "done"},
	)

	assert.Empty(t, bus.Drain(sessionID).Notices)
}

// A final answer larger than the event data bound is cut, marked, and still
// leaves valid JSON under the bound.
func TestAgentRunPublishCutsAnAnswerToTheDataBound(t *testing.T) {
	t.Parallel()

	const dataBound = 2048

	sessionID := uuid.New()
	bus := events.NewBus(events.Options{MaxDataBytes: dataBound})
	registry, err := NewAgentRunRegistry(
		sessionID,
		limitedBusPublisher{bus: bus},
		AgentRunLimits{},
	)
	require.NoError(t, err)

	testCases := []struct {
		name     string
		response string
	}{
		{name: "plain", response: strings.Repeat("a", 4*dataBound)},
		{name: "escaped", response: strings.Repeat("<\"", 4*dataBound)},
		{name: "multibyte", response: strings.Repeat("é", 4*dataBound)},
	}

	for _, tc := range testCases {
		run, _, err := registry.Start(context.Background(), StartAgentRunInput{
			Name: tc.name,
		})
		require.NoError(t, err)

		registry.Finish(
			context.Background(),
			run,
			AgentRunResult{State: AgentRunStateCompleted, Response: tc.response},
		)

		batch := bus.Drain(sessionID)
		require.Len(t, batch.Notices, 1, tc.name)
		require.NotEmpty(t, batch.Notices[0].Data, tc.name)
		assert.LessOrEqual(t, len(batch.Notices[0].Data), dataBound, tc.name)

		var data agentEventData
		require.NoError(t, json.Unmarshal(batch.Notices[0].Data, &data))
		assert.True(t, data.ResponseTruncated, tc.name)
		assert.True(t, utf8.ValidString(data.Response), tc.name)
		assert.True(
			t,
			strings.HasSuffix(data.Response, agentResultTruncatedNote),
			tc.name,
		)
	}
}

// limitedBusPublisher reports the bus's bounds the way the runtime's own
// publisher does, so the registry fits answers to them.
type limitedBusPublisher struct {
	bus *events.Bus
}

func (p limitedBusPublisher) PublishContext(
	ctx context.Context,
	notice events.Notice,
) (events.Notice, error) {
	return p.bus.PublishContext(ctx, notice)
}

func (p limitedBusPublisher) EventLimits() events.Limits {
	return p.bus.EventLimits()
}

// TestAgentRunPublishCarriesAFixedDuration pins the duration text of an
// agent completion notice for a known start and end time.
func TestAgentRunPublishCarriesAFixedDuration(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	bus := events.NewBus(events.Options{})
	registry, err := NewAgentRunRegistry(sessionID, bus, AgentRunLimits{})
	require.NoError(t, err)

	started := time.Date(2026, time.October, 10, 9, 0, 0, 0, time.UTC)
	run := &AgentRun{ID: uuid.New(), Name: "fixed", StartedAt: started}

	registry.publish(
		context.Background(),
		run,
		AgentRunResult{State: AgentRunStateCompleted},
		started.Add(90*time.Second),
	)

	batch := bus.Drain(sessionID)
	require.Len(t, batch.Notices, 1)

	var data agentEventData
	require.NoError(t, json.Unmarshal(batch.Notices[0].Data, &data))
	assert.Equal(t, int64(90000), data.DurationMs)
	assert.Equal(t, "1m30s", data.Duration)
}

func TestAgentRunEventBufferOverflowReportsDropped(t *testing.T) {
	t.Parallel()

	buffer := newAgentRunEventBuffer(2, 1<<20)
	buffer.Append("a", json.RawMessage(`{}`))
	buffer.Append("b", json.RawMessage(`{}`))
	buffer.Append("c", json.RawMessage(`{}`))

	gotEvents, next, dropped := buffer.Read(0, 0)
	require.Len(t, gotEvents, 2)
	assert.Equal(t, "b", gotEvents[0].Type)
	assert.Equal(t, "c", gotEvents[1].Type)
	assert.Equal(t, 1, dropped)
	assert.Equal(t, 3, next)

	buffered, totalDropped := buffer.Stats()
	assert.Equal(t, 2, buffered)
	assert.Equal(t, 1, totalDropped)
}

func TestAgentRunEventBufferOverflowByBytes(t *testing.T) {
	t.Parallel()

	buffer := newAgentRunEventBuffer(1000, 10)
	buffer.Append("type", json.RawMessage(`"01234"`))
	buffer.Append("type", json.RawMessage(`"56789"`))

	buffered, dropped := buffer.Stats()
	assert.Equal(t, 1, buffered, "the byte bound must evict the oldest event")
	assert.Equal(t, 1, dropped)
}

func TestAgentRunLimitsValidate(t *testing.T) {
	t.Parallel()

	valid := DefaultAgentRunLimits()
	require.NoError(t, valid.validate())

	invalid := valid
	invalid.MaxChildTurns = 0
	require.ErrorIs(t, invalid.validate(), ErrInvalidAgentRunLimits)
}

// Sanity check that this package's local sentinel wrapping still round-trips
// through errors.Is the way the rest of the codebase relies on.
func TestAgentRunSentinelsWrapCleanly(t *testing.T) {
	t.Parallel()

	wrapped := ctxerrors.Wrap(ErrTooManyAgentRuns, "context")
	assert.ErrorIs(t, wrapped, ErrTooManyAgentRuns)
}

// An ad-hoc agentDefinition and a stored agent file must share one
// instruction-byte bound per PLAN.md's "Named agents" contract. This locks
// the default to harness.Limits' MaxFileBytes default (128 KiB) so the two
// stay equal out of the box.
const (
	wantDefaultAgentDepth       = 5
	wantDefaultInstructionBytes = 128 * 1024
)

func TestDefaultAgentRunLimitsDepth(t *testing.T) {
	t.Parallel()

	assert.Equal(t, wantDefaultAgentDepth, DefaultAgentRunLimits().MaxDepth)
}

func TestDefaultAgentRunLimitsMatchesStoredFileByteBound(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		wantDefaultInstructionBytes,
		DefaultAgentRunLimits().MaxAdHocInstructionBytes,
	)
}

func TestPagingOptionsFromAPI(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		limit      *int32
		offset     *int32
		wantLimit  int
		wantOffset int
		wantErr    error
	}{
		{
			name:       "defaults when both are omitted",
			wantLimit:  session.DefaultPageLimit,
			wantOffset: 0,
		},
		{
			name:       "explicit values are honored",
			limit:      int32Ptr(10),
			offset:     int32Ptr(5),
			wantLimit:  10,
			wantOffset: 5,
		},
		{
			name:    "zero limit is rejected",
			limit:   int32Ptr(0),
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "limit over the maximum is rejected",
			limit:   int32Ptr(session.MaximumPageLimit + 1),
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "negative offset is rejected",
			offset:  int32Ptr(-1),
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotLimit, gotOffset, err := pagingOptionsFromAPI(tc.limit, tc.offset)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantLimit, gotLimit)
			assert.Equal(t, tc.wantOffset, gotOffset)
		})
	}
}

func TestPageWindow(t *testing.T) {
	t.Parallel()

	items := []int{0, 1, 2, 3, 4}

	testCases := []struct {
		name   string
		offset int
		limit  int
		want   []int
	}{
		{name: "first page", offset: 0, limit: 2, want: []int{0, 1}},
		{name: "middle page", offset: 2, limit: 2, want: []int{2, 3}},
		{name: "last partial page", offset: 4, limit: 2, want: []int{4}},
		{name: "offset past the end", offset: 5, limit: 2, want: nil},
		{name: "offset far past the end", offset: 99, limit: 2, want: nil},
		{name: "limit covers everything", offset: 0, limit: 99, want: items},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := pageWindow(items, tc.offset, tc.limit)
			assert.Equal(t, tc.want, got)
		})
	}
}

func int32Ptr(value int32) *int32 {
	return new(value)
}
