package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	streamDisconnectPoll    = 10 * time.Millisecond
	streamDisconnectTimeout = 10 * time.Second
)

// A client that goes away must not leave a turn running.
//
// This is the expensive failure mode of a streaming API: a closed tab, a
// dropped connection, or a proxy timeout leaves the provider call in flight,
// billing the deployment for output nobody will read, and holds the session
// lease so the same session cannot start another turn. The mechanism is that a
// write to the closed pipe fails, that error reaches Elelem through the
// publisher and the adapter's callback, and the run aborts.
func TestStreamDisconnectEndsTheTurnAndReleasesTheSession(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("an answer nobody is listening to"),
	))

	stream, err := fixture.runtime.StreamMessage(
		context.Background(),
		api.MessageRequest{Message: "stream then hang up"},
		nil,
		uuid.New(),
	)
	require.NoError(t, err)

	// Hang up without reading a single frame.
	require.NoError(t, stream.Body.Close())

	assert.Eventually(t, func() bool {
		return !fixture.store.IsActive(stream.SessionID)
	}, streamDisconnectTimeout, streamDisconnectPoll,
		"a disconnected stream must release the session lease")

	// The turn must also reach a terminal state on disk. A turn left running
	// is what RecoverInterrupted exists to clean up after a crash, and a
	// client hanging up is not a crash.
	state := disconnectedTurnState(t, fixture, stream.SessionID)
	assert.NotEqual(t, models.TurnStateRunning, state)

	// The session stays usable. Nothing about a dead client should stop the
	// next one.
	assert.False(t, fixture.store.IsActive(stream.SessionID))
}

// disconnectedTurnState reads the newest turn's durable state for a session.
func disconnectedTurnState(
	t *testing.T,
	fixture runtimeFixture,
	sessionID uuid.UUID,
) models.TurnState {
	t.Helper()

	query := repositories.Use(fixture.handle.GormDB)

	turn, err := query.Turn.WithContext(context.Background()).
		Where(query.Turn.SessionID.Eq(sessionID)).
		Order(query.Turn.StartedAt.Desc()).
		First()
	require.NoError(t, err)

	return turn.State
}
