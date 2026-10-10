package control

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/agent"
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
//
// Every worker event passes through here, which also makes the relay the place
// the controller learns that a worker admitted a message.
type EventRelay struct {
	mutex sync.RWMutex
	sink  EventSink

	admissionMutex   sync.Mutex
	admissionWatches map[admissionKey]*admissionWatch
}

// admissionKey names one message: the session it was sent to and its request.
type admissionKey struct {
	sessionID uuid.UUID
	requestID uuid.UUID
}

// admissionWatch is one caller waiting to hear a message was admitted. It is a
// pointer so a stop can tell its own watch from a later one under the same key.
type admissionWatch struct {
	admitted func()
}

// NewEventRelay builds an unattached relay.
func NewEventRelay() *EventRelay {
	return &EventRelay{
		admissionWatches: map[admissionKey]*admissionWatch{},
	}
}

// SetSink registers the live delivery path. Registering twice replaces it,
// which is what a restarted API service needs.
func (r *EventRelay) SetSink(sink EventSink) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.sink = sink
}

// WatchAdmission calls admitted once the session's worker admits the message
// sent under requestID.
//
// A worker records user_message.created for a message exactly when it admits
// it, whether the message starts a turn or joins the running turn's queue, and
// the record passes through this relay. admitted runs at most once, on the
// goroutine that publishes the event, so it must not block. The returned stop
// ends the watch and is safe to call after admitted has run.
func (r *EventRelay) WatchAdmission(
	sessionID uuid.UUID,
	requestID uuid.UUID,
	admitted func(),
) func() {
	key := admissionKey{sessionID: sessionID, requestID: requestID}
	watch := &admissionWatch{admitted: admitted}

	r.admissionMutex.Lock()
	r.admissionWatches[key] = watch
	r.admissionMutex.Unlock()

	return func() {
		r.admissionMutex.Lock()
		defer r.admissionMutex.Unlock()

		if r.admissionWatches[key] == watch {
			delete(r.admissionWatches, key)
		}
	}
}

// PublishSessionEvents delivers events that are already durable.
func (r *EventRelay) PublishSessionEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	events []session.EventInput,
) {
	r.reportAdmissions(sessionID, events)

	r.mutex.RLock()
	sink := r.sink
	r.mutex.RUnlock()

	if sink == nil || len(events) == 0 {
		return
	}

	sink(ctx, sessionID, events)
}

func (r *EventRelay) reportAdmissions(
	sessionID uuid.UUID,
	events []session.EventInput,
) {
	for _, event := range events {
		if event.EventType != agent.EventTypeUserMessageCreated {
			continue
		}

		key := admissionKey{sessionID: sessionID, requestID: event.RequestID}

		r.admissionMutex.Lock()
		watch, found := r.admissionWatches[key]
		delete(r.admissionWatches, key)
		r.admissionMutex.Unlock()

		if found {
			watch.admitted()
		}
	}
}
