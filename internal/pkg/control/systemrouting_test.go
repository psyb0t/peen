package control_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The session row is the system flag, so these seed it the way the registry
// does. Routing refuses a mismatch before any worker exists.
func openSystemRow(t *testing.T, fixture routingFixture) uuid.UUID {
	t.Helper()

	opened, err := fixture.store.OpenWorkspace(
		t.Context(),
		t.TempDir(),
		session.OpenSessionOptions{
			RootAgent:  harness.SystemAgentName,
			ModelID:    testDefaultModel,
			AgentSpace: worker.AgentSpaceControl,
		},
	)
	require.NoError(t, err)

	return opened.Session.ID
}

func TestRouterRefusesASystemFrameOnAnOrdinarySession(t *testing.T) {
	fixture := newRoutingFixture(t)

	_, err := fixture.router.RunSessionMessage(
		t.Context(),
		fixture.sessionID,
		agent.MessageRequest{Message: routedMessage, System: true},
		uuid.New(),
	)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Empty(t, fixture.launcher.workers)
}

func TestRouterRefusesAPlainFrameOnTheSystemSession(t *testing.T) {
	fixture := newRoutingFixture(t)

	_, err := fixture.router.RunSessionMessage(
		t.Context(),
		openSystemRow(t, fixture),
		agent.MessageRequest{Message: routedMessage},
		uuid.New(),
	)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Empty(t, fixture.launcher.workers)
}

// Text is never a trigger: only the structured field addresses the system
// session, so a message that merely says the command stays ordinary.
func TestRouterTreatsTheCommandTextAsAnOrdinaryMessage(t *testing.T) {
	fixture := newRoutingFixture(t)

	result, err := fixture.router.RunSessionMessage(
		t.Context(),
		fixture.sessionID,
		agent.MessageRequest{Message: `/system-agent "do it"`},
		uuid.New(),
	)
	require.NoError(t, err)
	assert.Equal(t, fixture.sessionID, result.SessionID)

	require.Len(t, fixture.launcher.workers, 1)
}
