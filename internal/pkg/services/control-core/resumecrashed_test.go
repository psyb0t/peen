package controlcore

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errResumeTestList   = ctxerrors.New("list failed")
	errResumeTestEnsure = ctxerrors.New("launch failed")
)

type fakeUnfinishedLister struct {
	sessions []*models.Session
	err      error
}

func (f fakeUnfinishedLister) ListSessionsWithUnfinishedExecutions(
	context.Context,
) ([]*models.Session, error) {
	return f.sessions, f.err
}

type ensureCall struct {
	sessionID  uuid.UUID
	workspace  string
	agentSpace string
}

type fakeEnsurer struct {
	calls  []ensureCall
	failOn uuid.UUID
}

func (f *fakeEnsurer) EnsureSessionWorker(
	_ context.Context,
	sessionID uuid.UUID,
	workspace string,
	agentSpaceName string,
) (*protocol.Worker, error) {
	f.calls = append(f.calls, ensureCall{
		sessionID: sessionID, workspace: workspace, agentSpace: agentSpaceName,
	})

	if sessionID == f.failOn {
		return nil, errResumeTestEnsure
	}

	return nil, nil //nolint:nilnil // The fake has no worker to return.
}

func TestResumeCrashedSessionsEnsuresEachWorkerAndSurvivesFailures(
	t *testing.T,
) {
	first := &models.Session{
		ID: uuid.New(), Workspace: "/work/a", AgentSpace: "control",
	}
	second := &models.Session{
		ID: uuid.New(), Workspace: "/work/b", AgentSpace: "docker:dev",
	}
	ensurer := &fakeEnsurer{failOn: first.ID}

	err := resumeCrashedSessions(t.Context(),
		fakeUnfinishedLister{sessions: []*models.Session{first, second}},
		ensurer)
	require.NoError(t, err)
	assert.Equal(t, []ensureCall{
		{sessionID: first.ID, workspace: "/work/a", agentSpace: "control"},
		{sessionID: second.ID, workspace: "/work/b", agentSpace: "docker:dev"},
	}, ensurer.calls)
}

func TestResumeCrashedSessionsReportsAListFailure(t *testing.T) {
	ensurer := &fakeEnsurer{}

	err := resumeCrashedSessions(t.Context(),
		fakeUnfinishedLister{err: errResumeTestList}, ensurer)
	require.ErrorIs(t, err, errResumeTestList)
	assert.Empty(t, ensurer.calls)
}
