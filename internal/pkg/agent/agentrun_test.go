package agent

import (
	"context"
	"encoding/json"
	"testing"

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

func TestAgentRunRegistryStartContextDescendsFromParent(t *testing.T) {
	t.Parallel()

	registry, err := NewAgentRunRegistry(uuid.New(), nil, AgentRunLimits{})
	require.NoError(t, err)

	parentCtx, cancel := context.WithCancel(context.Background())
	run, runCtx, err := registry.Start(parentCtx, StartAgentRunInput{
		Name:       "child",
		Definition: AgentRunDefinitionStored,
		Depth:      1,
	})
	require.NoError(t, err)
	assert.Equal(t, AgentRunStateRunning, run.Snapshot().State)
	assert.NoError(t, runCtx.Err())

	// Cancelling the parent context must cascade to the run's own context,
	// which is what makes cancelling a parent turn cancel the whole child
	// tree: the run's context is a plain context.WithCancel descendant.
	cancel()
	assert.Error(t, runCtx.Err())
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
	registry.Finish(context.Background(), first, AgentRunStateCompleted)

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

	registry.Finish(context.Background(), run, AgentRunStateCancelled)

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
	registry.Finish(context.Background(), older, AgentRunStateCompleted)

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
		name      string
		state     AgentRunState
		wantEvent events.Type
	}{
		{"completed", AgentRunStateCompleted, events.TypeAgentFinished},
		{"failed", AgentRunStateFailed, events.TypeAgentFailed},
		{"cancelled", AgentRunStateCancelled, events.TypeAgentFailed},
	}

	for _, tc := range testCases {
		run, _, err := registry.Start(context.Background(), StartAgentRunInput{
			Name: tc.name, Definition: AgentRunDefinitionStored, Depth: 1,
		})
		require.NoError(t, err)

		registry.Finish(context.Background(), run, tc.state)

		batch := bus.Drain(sessionID)
		require.Len(t, batch.Notices, 1)
		assert.Equal(t, tc.wantEvent, batch.Notices[0].Type)

		var data agentEventData
		require.NoError(t, json.Unmarshal(batch.Notices[0].Data, &data))
		assert.Equal(t, run.ID, data.RunID)
		assert.Equal(t, tc.name, data.AgentName)
	}
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
const wantDefaultInstructionBytes = 128 * 1024

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
