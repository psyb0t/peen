package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/require"
)

const (
	sessionStartScriptMode = 0o700
	sessionStartRunMarker  = "session-start-ran"
)

// sessionStartResumedFixture mimics the controller and worker split: the
// session row exists before the runtime attaches to it by ID, so the runtime
// never sees Created=true for it.
type sessionStartResumedFixture struct {
	runtimeFixture

	options    RuntimeOptions
	sessionID  uuid.UUID
	markerPath string
}

func newSessionStartResumedFixture(
	t *testing.T,
	turns ...elelemtest.Turn,
) sessionStartResumedFixture {
	t.Helper()

	var (
		captured  RuntimeOptions
		sessionID uuid.UUID
	)

	fixture := newRuntimeFixtureWithOptions(
		t,
		elelemtest.NewScriptedDriver(turns...),
		func(options *RuntimeOptions) {
			opened, err := options.Store.OpenWorkspace(
				context.Background(),
				options.DefaultWorkspace,
				session.OpenSessionOptions{
					RootAgent: options.RootAgent,
					ModelID:   options.DefaultModel,
				},
			)
			require.NoError(t, err)

			sessionID = opened.Session.ID
			options.StartupSessionID = sessionID
			captured = *options
		},
	)

	scriptRoot := t.TempDir()
	markerPath := filepath.Join(scriptRoot, "marker.log")
	scriptPath := filepath.Join(scriptRoot, "mark.sh")

	require.NoError(t, os.WriteFile(
		scriptPath,
		[]byte("#!/bin/sh\ncat > /dev/null\necho "+sessionStartRunMarker+" >> "+markerPath+"\n"),
		sessionStartScriptMode,
	))

	writeChildHarnessHooks(t, fixture, `version: 1
session_start:
  - name: mark
    actions:
      - type: command
        command: `+scriptPath+`
`)

	return sessionStartResumedFixture{
		runtimeFixture: fixture,
		options:        captured,
		sessionID:      sessionID,
		markerPath:     markerPath,
	}
}

func (f sessionStartResumedFixture) runCount(t *testing.T) int {
	t.Helper()

	raw, err := os.ReadFile(f.markerPath)
	if os.IsNotExist(err) {
		return 0
	}

	require.NoError(t, err)

	return strings.Count(string(raw), sessionStartRunMarker)
}

func (f sessionStartResumedFixture) runTurn(t *testing.T, runtime *Runtime) {
	t.Helper()

	result, err := runtime.Run(context.Background(), TurnRequest{
		Message:   "hello",
		Workspace: f.workspace,
	})
	require.NoError(t, err)
	require.Equal(t, f.sessionID, result.SessionID)
}

// The controller creates the session and the worker resumes it, so the
// runtime never created the row. session_start still has to run on the
// session's first turn, and only on that one.
func TestSessionStartRunsOnFirstTurnOfResumedSession(t *testing.T) {
	fixture := newSessionStartResumedFixture(
		t,
		elelemtest.Text("one"),
		elelemtest.Text("two"),
	)

	fixture.runTurn(t, fixture.runtime)
	require.Equal(t, 1, fixture.runCount(t), "first turn must fire session_start")

	fixture.runTurn(t, fixture.runtime)
	require.Equal(t, 1, fixture.runCount(t), "second turn must not fire it again")
}

// A worker restart loses process memory, so the once-per-session guarantee has
// to come from the durable turn history.
func TestSessionStartDoesNotRunAgainAfterRuntimeRestart(t *testing.T) {
	fixture := newSessionStartResumedFixture(
		t,
		elelemtest.Text("one"),
		elelemtest.Text("two"),
	)

	fixture.runTurn(t, fixture.runtime)
	require.Equal(t, 1, fixture.runCount(t))

	restarted, err := NewRuntime(context.Background(), fixture.options)
	require.NoError(t, err)

	fixture.runTurn(t, restarted)
	require.Equal(t, 1, fixture.runCount(t), "a restart must not replay session_start")
}
