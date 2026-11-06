package supervisor_test

import (
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testStateDirectory = "/srv/peen/state"

func enableSystemAgent(options *supervisor.Options) {
	options.SystemAgent = true
	options.StateDirectory = testStateDirectory
}

func openSystemSession(t *testing.T, f fixture) *session.OpenSessionResult {
	t.Helper()

	opened, err := f.store.OpenWorkspace(
		t.Context(),
		t.TempDir(),
		session.OpenSessionOptions{
			RootAgent:  harness.SystemAgentName,
			ModelID:    "provider/model",
			AgentSpace: testSpaceDocker,
		},
	)
	require.NoError(t, err)

	return opened
}

// The session row decides, so a caller naming any agent space still gets the
// control worker, and that worker alone learns the real directories.
func TestSystemSessionRunsInControlWithItsLaunchPaths(t *testing.T) {
	f := newFixture(t, true, enableSystemAgent)
	opened := openSystemSession(t, f)

	live, err := f.supervisor.Ensure(t.Context(), supervisor.EnsureRequest{
		SessionID:      opened.Session.ID,
		Workspace:      opened.Session.Workspace,
		AgentSpaceName: testSpaceDocker,
	})
	require.NoError(t, err)
	require.NotNil(t, live)

	require.Len(t, f.launcher.workers, 1)

	document := f.launcher.workers[0].document
	assert.Equal(t, testSpaceControl, document.AgentSpace)
	require.NotNil(t, document.System)
	assert.Equal(t, testStateDirectory, document.System.StateDirectory)
	assert.Equal(t, document.ConfigDirectory, document.System.ConfigDirectory)
}

func TestSystemSessionStaysOffWithoutTheSetting(t *testing.T) {
	f := newFixture(t, true)
	opened := openSystemSession(t, f)

	_, err := f.supervisor.Ensure(t.Context(), supervisor.EnsureRequest{
		SessionID: opened.Session.ID,
		Workspace: opened.Session.Workspace,
	})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Zero(t, f.launcher.launched)
}

func TestOrdinarySessionGetsNoSystemLaunch(t *testing.T) {
	f := newFixture(t, true, enableSystemAgent)

	_, err := f.supervisor.Ensure(t.Context(), supervisor.EnsureRequest{
		SessionID:      f.sessionID,
		Workspace:      f.workspace,
		AgentSpaceName: testSpaceControl,
	})
	require.NoError(t, err)

	require.Len(t, f.launcher.workers, 1)
	assert.Nil(t, f.launcher.workers[0].document.System)
}
