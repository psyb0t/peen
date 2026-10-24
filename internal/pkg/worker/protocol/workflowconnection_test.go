package protocol

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/stretchr/testify/require"
)

func TestWorkflowConnectionUsesRegisteredSession(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	connection := &Connection{
		conn: fixture.remote.conn,
		Ack:  HelloAck{SessionID: fixture.sessionID},
	}
	remote := connection.Workflows()
	require.NoError(t, remote.PrepareWorkflowQueues(t.Context(),
		[]workflow.Queue{workflow.Queue(fixture.sessionID.String())}))
	require.ErrorIs(t, remote.PrepareWorkflowQueues(t.Context(),
		[]workflow.Queue{workflow.Queue(uuid.NewString())}),
		commerr.ErrPermissionDenied)
}
