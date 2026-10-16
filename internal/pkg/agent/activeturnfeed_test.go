package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heldAcquireTurnStore blocks one in-flight Run after its turn lease is
// acquired, so a test can observe the session while a turn is active rather
// than only before it starts or after it ends.
type heldAcquireTurnStore struct {
	*session.Store

	acquired chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (s *heldAcquireTurnStore) AcquireTurn(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.StartTurnInput,
) (session.Lease, error) {
	lease, err := s.Store.AcquireTurn(ctx, sessionID, input)
	if err != nil {
		return lease, err
	}

	s.once.Do(func() {
		close(s.acquired)
		<-s.release
	})

	return lease, nil
}

// GetSession reports FeedSequence and, while a turn runs, ActiveTurnFeedSequence
// as the first feed sequence of that turn. Both come from the same source
// IsActive already uses, so they track the same turn.
func TestRuntimeSessionReportsFeedSequenceAndActiveTurnFeedSequence(t *testing.T) {
	ctx := context.Background()
	fixture := newRuntimeFixture(
		t,
		elelemtest.NewScriptedDriver(elelemtest.Text("done")),
	)

	opened, err := fixture.store.OpenWorkspace(
		ctx,
		fixture.workspace,
		session.OpenSessionOptions{
			RootAgent: runtimeTestAgentName,
			ModelID:   runtimeTestModelReference,
		},
	)
	require.NoError(t, err)

	sessionID := opened.Session.ID

	before, err := fixture.runtime.Session(ctx, sessionID)
	require.NoError(t, err)
	assert.False(t, before.ActiveTurn)
	assert.Nil(t, before.ActiveTurnFeedSequence)
	assert.Zero(t, before.FeedSequence)

	held := &heldAcquireTurnStore{
		Store:    fixture.store,
		acquired: make(chan struct{}),
		release:  make(chan struct{}),
	}
	fixture.runtime.store = held

	runDone := make(chan error, 1)
	go func() {
		_, runErr := fixture.runtime.Run(ctx, TurnRequest{
			SessionID: &sessionID,
			Message:   "do the work",
			Workspace: fixture.workspace,
		})
		runDone <- runErr
	}()

	select {
	case <-held.acquired:
	case runErr := <-runDone:
		t.Fatalf("turn finished before its lease could be observed: %v", runErr)
	}

	during, err := fixture.runtime.Session(ctx, sessionID)
	require.NoError(t, err)
	assert.True(t, during.ActiveTurn)
	require.NotNil(t, during.ActiveTurnFeedSequence)
	assert.Positive(t, *during.ActiveTurnFeedSequence)
	assert.GreaterOrEqual(t, during.FeedSequence, *during.ActiveTurnFeedSequence)

	close(held.release)
	require.NoError(t, <-runDone)

	after, err := fixture.runtime.Session(ctx, sessionID)
	require.NoError(t, err)
	assert.False(t, after.ActiveTurn)
	assert.Nil(t, after.ActiveTurnFeedSequence)
	assert.Positive(t, after.FeedSequence)
}
