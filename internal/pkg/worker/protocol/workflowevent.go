package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// workflowEvent keeps the library's history format while checking the event
// before its polymorphic attributes reach the native backend.
type workflowEvent struct {
	*history.Event
}

// The field tags belong to go-workflows, which owns the persisted format.
type encodedWorkflowEvent struct {
	ID              string            `json:"id,omitempty"`
	SequenceID      int64             `json:"sid,omitempty"`
	Type            history.EventType `json:"t,omitempty"`
	Timestamp       time.Time         `json:"ts"`
	ScheduleEventID int64             `json:"seid,omitempty"`
	Attributes      json.RawMessage   `json:"attr,omitempty"`
	VisibleAt       *time.Time        `json:"vat,omitempty"`
}

func (e workflowEvent) MarshalJSON() ([]byte, error) {
	encoded, err := json.Marshal(e.Event)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode workflow event")
	}

	return encoded, nil
}

func (e *workflowEvent) UnmarshalJSON(data []byte) error {
	var encoded encodedWorkflowEvent
	if err := json.Unmarshal(data, &encoded); err != nil {
		return ctxerrors.Wrap(
			errors.Join(commerr.ErrValidationFailed, err),
			"decode workflow event",
		)
	}

	if err := encoded.validate(); err != nil {
		return ctxerrors.Wrap(err, "validate workflow event")
	}

	decoded, err := history.DeserializeAttributes(
		encoded.Type, encoded.Attributes,
	)
	if err != nil {
		return ctxerrors.Wrap(
			errors.Join(commerr.ErrValidationFailed, err),
			"decode workflow event attributes",
		)
	}

	// The library returns pointers for every supported attribute type.
	if decoded == nil || reflect.ValueOf(decoded).IsNil() {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"workflow event attributes are required",
		)
	}

	e.Event = &history.Event{
		ID: encoded.ID, SequenceID: encoded.SequenceID,
		Type: encoded.Type, Timestamp: encoded.Timestamp,
		ScheduleEventID: encoded.ScheduleEventID,
		Attributes:      decoded, VisibleAt: encoded.VisibleAt,
	}

	return nil
}

func (e encodedWorkflowEvent) validate() error {
	id, err := uuid.Parse(e.ID)
	if err != nil || id == uuid.Nil {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed, "workflow event identity",
		)
	}

	attributes := bytes.TrimSpace(e.Attributes)
	if e.Timestamp.IsZero() || e.SequenceID < 0 ||
		e.ScheduleEventID < 0 || len(attributes) == 0 ||
		attributes[0] != '{' {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed, "workflow event shape",
		)
	}

	return nil
}
