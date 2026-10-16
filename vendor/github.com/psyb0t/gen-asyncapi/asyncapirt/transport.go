package asyncapirt

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// Transport moves whole messages between the generated client and the server.
// ReadMessage blocks until a message arrives or ctx ends. Implementations must
// allow one goroutine reading while others write.
type Transport interface {
	WriteMessage(ctx context.Context, data []byte) error
	ReadMessage(ctx context.Context) ([]byte, error)
	Close() error
}

// Send validates payload, encodes it as JSON and writes it to transport.
func Send(ctx context.Context, transport Transport, payload any) error {
	if err := ValidateValue(payload); err != nil {
		return err
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"encode message: %v",
			err,
		)
	}

	if err := transport.WriteMessage(ctx, data); err != nil {
		return ctxerrors.Wrap(err, "write message")
	}

	return nil
}
