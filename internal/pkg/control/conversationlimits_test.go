package control_test

import (
	"context"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConversationReportsRoundAndTokenLimits(t *testing.T) {
	for _, limit := range []error{session.ErrTurnRoundLimit, session.ErrTurnTokenLimit} {
		t.Run(limit.Error(), func(t *testing.T) {
			fixture := newRoutingFixture(t)
			fixture.launcher.onTurn = func(ctx context.Context, w *routedWorker,
				request protocol.RunTurn,
			) (any, error) {
				lease, err := acquireConversationTurn(ctx, w, request)
				if err != nil {
					return nil, err
				}
				//nolint:contextcheck // Lease release carries no context.
				defer w.connection.Store().ReleaseTurn(lease)
				if err := w.connection.Store().FinalizeTurn(ctx, lease,
					session.FinalizeTurnInput{State: models.TurnStateFailed}); err != nil {
					return nil, err
				}

				return nil, limit
			}
			conversation, err := control.NewConversation(fixture.registry, fixture.router, fixture.feed)
			require.NoError(t, err)
			result, err := conversation.Run(t.Context(), control.ConversationRequest{
				SessionID: fixture.sessionID,
				Message:   agent.MessageRequest{Message: routedMessage},
			})
			require.NoError(t, err)
			assert.True(t, result.LengthLimited)
			assert.EqualValues(t, models.TurnStateFailed, result.State)
		})
	}
}

func TestRegistryReusesWorkspaceSessionAndChecksPolicy(t *testing.T) {
	fixture := newRoutingFixture(t)
	for range 2 {
		opened, err := fixture.registry.Open(t.Context(), fixture.launcher.workspace, "")
		require.NoError(t, err)
		assert.False(t, opened.Created)
		assert.Equal(t, fixture.sessionID, opened.Session.ID)
		assert.Equal(t, fixture.launcher.workspace, opened.Session.Workspace)
	}
	canonical, err := fixture.registry.Open(t.Context(), fixture.launcher.workspace, "")
	require.NoError(t, err)
	assert.Equal(t, fixture.sessionID, canonical.Session.ID)
	_, err = fixture.registry.Open(t.Context(), canonicalTempDir(t), "")
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = fixture.registry.Open(t.Context(), fixture.launcher.workspace, "missing")
	require.Error(t, err)
	conversation, err := control.NewConversation(fixture.registry, fixture.router, fixture.feed)
	require.NoError(t, err)
	_, err = conversation.Run(t.Context(), control.ConversationRequest{
		SessionID: fixture.sessionID, AgentSpace: "control",
		Message: agent.MessageRequest{Message: routedMessage},
	})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Empty(t, fixture.launcher.workers)
}
