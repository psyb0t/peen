package telegram

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// Never wrap external errors. net/http embeds the credential-bearing URL in
// url.Error, and bodies and transport errors may contain arbitrary secrets.
func requestFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "Telegram request ended")
	}

	return ctxerrors.Wrap(commerr.ErrUnavailable, "Telegram request failed")
}

func invalidInput() error {
	return ctxerrors.Wrap(commerr.ErrValidationFailed,
		"invalid Telegram client input")
}

func invalidResponse() error {
	return ctxerrors.Wrap(commerr.ErrInvalidState,
		"invalid Telegram response")
}

func sizeFailure() error {
	return ctxerrors.Wrap(commerr.ErrOutOfRange,
		"Telegram content exceeds its size limit")
}
