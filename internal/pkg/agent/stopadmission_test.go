package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type heldSessionReadStore struct {
	*session.Store
	read    chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *heldSessionReadStore) Get(ctx context.Context,
	sessionID uuid.UUID,
) (*models.Session, error) {
	stored, err := s.Store.Get(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read session for interleaving")
	}
	s.once.Do(func() {
		close(s.read)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	})
	if err := ctx.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "release held session read")
	}

	return stored, nil
}

func TestRuntimeStopRefusesOpeningPreparedFromEarlierRevision(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("new work completed"))
	fixture := newRuntimeFixture(t, driver)
	opened, err := fixture.store.OpenWorkspace(t.Context(), fixture.workspace,
		session.OpenSessionOptions{
			RootAgent: runtimeTestAgentName,
			ModelID:   runtimeTestModelReference,
		})
	require.NoError(t, err)
	sessionID := opened.Session.ID
	notice, err := fixture.runtime.persistAndPublishNotice(t.Context(),
		events.Notice{
			SessionID: sessionID, Type: "app.ready", Source: "example",
			Summary: "retained event", Delivery: events.DeliveryQueue,
		})
	require.NoError(t, err)
	held := &heldSessionReadStore{
		Store: fixture.store,
		read:  make(chan struct{}), release: make(chan struct{}),
	}
	fixture.runtime.store = held
	ctx, cancel := context.WithTimeout(t.Context(), runtimeTestTurnTimeout)
	t.Cleanup(cancel)
	release := sync.OnceFunc(func() { close(held.release) })
	t.Cleanup(release)
	result := make(chan error, 1)
	input := TurnRequest{
		SessionID: &sessionID, RequestID: uuid.New(),
		Workspace: fixture.workspace, Message: "old request",
	}
	go func() {
		defer func() {
			if value := recover(); value != nil {
				result <- ctxerrors.Wrapf(commerr.ErrInvalidState,
					"runtime request panicked: %v", value)
			}
		}()
		_, err := fixture.runtime.Run(ctx, input)
		result <- err
	}()
	select {
	case <-held.read:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = fixture.runtime.CancelSession(t.Context(), sessionID)
	require.NoError(t, err)
	release()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assert.Zero(t, driver.Calls())
	assert.False(t, fixture.store.IsActive(sessionID))
	pending, err := fixture.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	require.Len(t, pending.Items, 1)
	assert.Equal(t, notice.ID, pending.Items[0].ID)
	assert.Equal(t, 1, fixture.eventBus.Pending(sessionID))
	input.RequestID, input.Message = uuid.New(), "new task"
	continued, err := fixture.runtime.Run(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "new work completed", continued.Text)
	assert.Equal(t, 1, driver.Calls())
}
