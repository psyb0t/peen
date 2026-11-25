package wshub

import (
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
)

// EventHandler processes events with access to the hub, client, and event,
// and returns an error if processing fails.
type EventHandler func(hub Hub, client *Client, event *dabluveees.Event) error

// InvalidEventHandler handles one message that did not decode as an event.
// It runs on the connection's read pump, so it should return quickly. hub is
// the client's hub and may be nil for a client outside a hub. data is the raw
// message, at most the client's read limit; err is the decode error. Answer
// the sender with client.SendEvent or hub.BroadcastToClients. Return nil to
// keep the connection open, or an error to close it with code 1007.
type InvalidEventHandler func(
	hub Hub, client *Client, data []byte, err error,
) error

// EventTypeEchoRequestHandler handles echo request events by creating an
// echo reply with the same data and setting the original request event ID as
// triggeredBy.
func EventTypeEchoRequestHandler(
	_ Hub,
	client *Client,
	event *dabluveees.Event,
) error {
	// Create echo reply event with same data
	replyEvent := dabluveees.NewEvent(
		dabluveees.EventTypeEchoReply,
		event.Data,
	).SetTriggeredBy(event.ID)

	// Send the reply back to the client that sent the request
	client.SendEvent(&replyEvent)

	return nil
}
