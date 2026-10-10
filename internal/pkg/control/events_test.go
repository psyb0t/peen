package control_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
)

// The router lets a session's next message in when the worker admits the
// current one. The relay reports exactly that event, for exactly that
// message, once, whether or not a live sink is registered yet.
func TestEventRelayReportsAdmissionOfTheWatchedMessage(t *testing.T) {
	t.Parallel()

	relay := control.NewEventRelay()
	sessionID := uuid.New()
	requestID := uuid.New()

	admissions := 0
	stop := relay.WatchAdmission(sessionID, requestID, func() { admissions++ })
	t.Cleanup(stop)

	unrelated := []session.EventInput{
		{RequestID: requestID, EventType: agent.EventTypeTurnStarted},
		{RequestID: uuid.New(), EventType: agent.EventTypeUserMessageCreated},
	}
	relay.PublishSessionEvents(context.Background(), sessionID, unrelated)
	relay.PublishSessionEvents(
		context.Background(),
		uuid.New(),
		[]session.EventInput{
			{RequestID: requestID, EventType: agent.EventTypeUserMessageCreated},
		},
	)
	assert.Zero(t, admissions, "another event, message, or session admitted it")

	admitted := []session.EventInput{
		{RequestID: requestID, EventType: agent.EventTypeUserMessageCreated},
	}
	relay.PublishSessionEvents(context.Background(), sessionID, admitted)
	relay.PublishSessionEvents(context.Background(), sessionID, admitted)
	assert.Equal(t, 1, admissions)
}

// A stopped watch hears nothing, so a message that already finished cannot
// release a reservation it no longer holds.
func TestEventRelayStoppedWatchHearsNothing(t *testing.T) {
	t.Parallel()

	relay := control.NewEventRelay()
	sessionID := uuid.New()
	requestID := uuid.New()

	admissions := 0
	stop := relay.WatchAdmission(sessionID, requestID, func() { admissions++ })
	stop()

	relay.PublishSessionEvents(
		context.Background(),
		sessionID,
		[]session.EventInput{
			{RequestID: requestID, EventType: agent.EventTypeUserMessageCreated},
		},
	)
	assert.Zero(t, admissions)
}
