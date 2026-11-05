package session

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type observedKinds struct {
	mutex        sync.Mutex
	observations []metrics.Observation
}

func (o *observedKinds) add(observation metrics.Observation) {
	o.mutex.Lock()
	defer o.mutex.Unlock()

	o.observations = append(o.observations, observation)
}

func (o *observedKinds) ofKind(
	kind metrics.ObservationKind,
) []metrics.Observation {
	o.mutex.Lock()
	defer o.mutex.Unlock()

	matched := []metrics.Observation{}

	for _, observation := range o.observations {
		if observation.Kind == kind {
			matched = append(matched, observation)
		}
	}

	return matched
}

func openObservedStore(t *testing.T) (*Store, *observedKinds) {
	t.Helper()

	handle, err := db.Open(
		context.Background(),
		db.Config{Directory: t.TempDir()},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	collector := metrics.New()
	observed := &observedKinds{}
	collector.SetSink(observed.add)

	store, err := NewStore(handle, Options{Metrics: collector})
	require.NoError(t, err)

	return store, observed
}

func TestTurnLifecycleIsObservedFromItsDurableWrites(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		state   models.TurnState
		outcome string
	}{
		{"completed", models.TurnStateCompleted, metrics.OutcomeSuccess},
		{"failed", models.TurnStateFailed, metrics.OutcomeError},
		{"cancelled", models.TurnStateCancelled, metrics.OutcomeCancelled},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store, observed := openObservedStore(t)
			sessionRow := newTestSession(ctx, t, store)
			generationID := uuid.New().String()

			lease, err := store.AcquireTurn(ctx, sessionRow.ID, StartTurnInput{
				RequestID:          uuid.New(),
				Workspace:          testWorkspace,
				WorkerGenerationID: generationID,
			})
			require.NoError(t, err)

			started := observed.ofKind(metrics.KindTurnStarted)
			require.Len(t, started, 1)
			assert.Equal(t, lease.TurnID.String(), started[0].TurnID)

			require.NoError(t, store.FinalizeTurn(ctx, lease, FinalizeTurnInput{
				State: tc.state,
			}))

			finished := observed.ofKind(metrics.KindTurn)
			require.Len(t, finished, 1)
			assert.Equal(t, tc.outcome, finished[0].Outcome)
			assert.Equal(t, sessionRow.ID.String(), finished[0].SessionID)
			assert.Equal(t, generationID, finished[0].WorkerGenerationID)
			assert.GreaterOrEqual(t, finished[0].DurationMS, int64(0))
		})
	}
}

func TestRolledBackWriteIsNeverObserved(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, observed := openObservedStore(t)

	err := store.feedTransaction(ctx, func(
		ctx context.Context,
		_ *repositories.Query,
	) error {
		store.observe(ctx, metrics.Observation{Kind: metrics.KindTurn})

		return commerr.ErrConflict
	})

	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Empty(t, observed.ofKind(metrics.KindTurn))
}

func TestCompactionIsObservedAfterItCommits(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, observed := openObservedStore(t)
	agentRunID := uuid.New()

	store.observeCompaction(ctx, uuid.New(), nil, 100, 10, 4)
	store.observeCompaction(ctx, uuid.New(), &agentRunID, 50, 5, 2)

	compactions := observed.ofKind(metrics.KindCompaction)
	require.Len(t, compactions, 2)
	assert.Empty(t, compactions[0].AgentRunID)
	assert.Equal(t, int64(100), compactions[0].InputTokens)
	assert.Equal(t, int64(10), compactions[0].OutputTokens)
	assert.Equal(t, int64(4), compactions[0].Count)
	assert.Equal(t, agentRunID.String(), compactions[1].AgentRunID)
}

func TestOutcomesCoverEveryTerminalState(t *testing.T) {
	t.Parallel()

	assert.Equal(t, metrics.OutcomeSuccess,
		agentRunOutcome(models.AgentRunStateCompleted))
	assert.Equal(t, metrics.OutcomeCancelled,
		agentRunOutcome(models.AgentRunStateCancelled))
	assert.Equal(t, metrics.OutcomeError,
		agentRunOutcome(models.AgentRunStateFailed))
	assert.Equal(t, metrics.OutcomeError,
		agentRunOutcome(models.AgentRunStateInterrupted))
}
