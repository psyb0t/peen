package control_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/require"
)

func TestIntegrationSessionGuardRefusesSystemAndOutsideWorkspaces(t *testing.T) {
	fixture := newRoutingFixture(t)
	require.NoError(t, fixture.registry.ValidateWorkspaceSession(t.Context(), fixture.sessionID))
	require.ErrorIs(t, fixture.registry.ValidateWorkspaceSession(t.Context(), uuid.New()), commerr.ErrNotFound)
	for _, rootAgent := range []string{testRootAgent, harness.SystemAgentName} {
		stored, err := fixture.store.OpenWorkspace(t.Context(), canonicalTempDir(t), session.OpenSessionOptions{
			RootAgent: rootAgent, ModelID: testDefaultModel,
		})
		require.NoError(t, err)
		require.ErrorIs(t, fixture.registry.ValidateWorkspaceSession(t.Context(), stored.Session.ID), commerr.ErrPermissionDenied)
	}
}
