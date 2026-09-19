package control

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// EventSink delivers one session's durable events to live clients.
type EventSink func(
	ctx context.Context,
	sessionID uuid.UUID,
	events []session.EventInput,
)

// EventRelay carries worker events from the durable write to the live feed.
//
// It exists because the two halves come up in order: control-core opens the
// store and the worker socket, and control-api brings up the WebSocket hub
// afterwards. The relay is created with the first and its sink registered by
// the second, so neither service has to import the other.
//
// Events reach it only after the controller has written them, so a client can
// never see an event the database does not already hold. Before a sink is
// registered, publishing is a no-op rather than an error: a worker's durable
// write must not fail because nothing is listening yet.
type EventRelay struct {
	mutex sync.RWMutex
	sink  EventSink
}

// NewEventRelay builds an unattached relay.
func NewEventRelay() *EventRelay {
	return &EventRelay{}
}

// SetSink registers the live delivery path. Registering twice replaces it,
// which is what a restarted API service needs.
func (r *EventRelay) SetSink(sink EventSink) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.sink = sink
}

// PublishSessionEvents delivers events that are already durable.
func (r *EventRelay) PublishSessionEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	events []session.EventInput,
) {
	r.mutex.RLock()
	sink := r.sink
	r.mutex.RUnlock()

	if sink == nil || len(events) == 0 {
		return
	}

	sink(ctx, sessionID, events)
}
