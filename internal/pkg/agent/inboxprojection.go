package agent

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// Collect stored inbox records without writing those records again.
func (t *runtimeTurn) projectStoredInbox(
	ctx context.Context,
	receipt *session.ExecutionInboxDelivery,
) error {
	events, err := inboxReceiptEvents(receipt.Events)
	if err != nil {
		return ctxerrors.Wrap(err, "decode retained inbox events")
	}

	if err := t.retainStoredInbox(ctx, receipt.Messages, events); err != nil {
		return ctxerrors.Wrap(err, "retain inbox projection")
	}

	t.mutex.Lock()
	sink := t.sink
	t.mutex.Unlock()

	for _, event := range events {
		if t.observer != nil {
			t.observer(event)
		}

		if sink != nil {
			if err := sink(event); err != nil {
				return ctxerrors.Wrap(err, "publish retained inbox event")
			}
		}
	}

	return nil
}

func (t *runtimeTurn) retainStoredInbox(
	ctx context.Context,
	messages []session.MessageInput,
	events []Event,
) error {
	t.checkpointMutex.Lock()
	defer t.checkpointMutex.Unlock()

	pending := t.pendingTranscript()
	if !pending.isEmpty() {
		if err := t.store.AppendCheckpoint(context.WithoutCancel(ctx), t.lease,
			pending.messages, pending.events); err != nil {
			return ctxerrors.Wrap(err, "checkpoint pending inbox projection")
		}
	}

	t.mutex.Lock()
	defer t.mutex.Unlock()

	// A concurrent callback can append a tail while the checkpoint is written.
	t.messages = slices.Insert(t.messages, pending.nextMessages, messages...)
	t.events = slices.Insert(t.events, pending.nextEvents, events...)
	t.checkpointedMessages = pending.nextMessages + len(messages)
	t.checkpointedEvents = pending.nextEvents + len(events)

	return nil
}

func inboxReceiptEvents(inputs []session.EventInput) ([]Event, error) {
	events := make([]Event, 0, len(inputs))
	for _, input := range inputs {
		var payload struct {
			SourceEventID uuid.UUID `json:"sourceEventId"`
		}
		if err := json.Unmarshal([]byte(input.PayloadJSON),
			&payload); err != nil {
			return nil, ctxerrors.Wrap(err, "decode inbox event origin")
		}

		events = append(events, Event{
			Type: input.EventType, Payload: json.RawMessage(input.PayloadJSON),
			RequestID:         input.RequestID,
			TriggeringEventID: payload.SourceEventID,
		})
	}

	return events, nil
}
