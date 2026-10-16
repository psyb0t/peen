package server

import (
	"context"
	"encoding/json"

	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
)

// validateServerFrame checks one rendered outgoing WebSocket frame against
// the generated wsapi contract.
//
// It never withholds the frame: a contract bug in Peen's own emitter must not
// create a gap in a client's live stream, so a mismatch is only logged, and
// every caller sends the frame exactly as it would have otherwise.
func validateServerFrame(ctx context.Context, event *dabluveees.Event) {
	if event == nil {
		return
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		logServerFrameContractFailure(
			ctx,
			event,
			ctxerrors.Wrap(err, "encode server frame for contract check"),
		)

		return
	}

	target := wsapi.ServerEvent{}
	if err := target.UnmarshalJSON(encoded); err != nil {
		logServerFrameContractFailure(
			ctx,
			event,
			ctxerrors.Wrap(err, "validate server frame contract"),
		)
	}
}

// logServerFrameContractFailure logs a frame's contract mismatch with the
// frame type, its session id and sequence when the frame carries them, and
// the validation error.
func logServerFrameContractFailure(
	ctx context.Context,
	event *dabluveees.Event,
	err error,
) {
	fields := []any{
		"frame_type", string(event.Type),
		"err", err,
	}

	if sessionID, ok := eventMetadataString(
		event,
		webSocketMetadataSessionID,
	); ok {
		fields = append(fields, "session_id", sessionID)
	}

	if sequence, ok := eventMetadataSequence(event); ok {
		fields = append(fields, "sequence", sequence)
	}

	ctxscope.GetLogger(ctx).Error(
		"server frame failed contract validation",
		fields...,
	)
}

func eventMetadataString(
	event *dabluveees.Event,
	key string,
) (string, bool) {
	if event.Metadata == nil {
		return "", false
	}

	value, found := event.Metadata.Get(key)
	if !found {
		return "", false
	}

	text, ok := value.(string)

	return text, ok
}

func eventMetadataSequence(event *dabluveees.Event) (int64, bool) {
	if event.Metadata == nil {
		return 0, false
	}

	value, found := event.Metadata.Get(webSocketMetadataSequence)
	if !found {
		return 0, false
	}

	sequence, ok := value.(int64)

	return sequence, ok
}
