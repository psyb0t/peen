package agent

import (
	"context"
	"io"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const reasonProviderStreamCut = "provider_stream_without_finish_reason"

// finishReasonDriver turns a stream that ends without a finish reason into an
// error. A connection a proxy or the provider closes cleanly mid-response
// reads as the end of the stream to the driver, which reports success with an
// unset finish reason and whatever text arrived, so the engine would take a
// cut answer, or an empty one, for the final one.
//
// The error wraps io.ErrUnexpectedEOF, which the retry decorator around this
// driver treats as a transport failure: a cut before the first delta is
// retried, and a cut after output has reached the caller fails the call.
//
// Only Stream is checked. Complete reads a whole response body, so a missing
// reason there is an unmapped provider value, not a cut connection.
type finishReasonDriver struct {
	elelem.Driver
}

// requireFinishReason wraps driver in finishReasonDriver. Call it before the
// retry decorator so a cut stream is seen by the retry policy.
//
//nolint:ireturn // Elelem's driver contract is an interface.
func requireFinishReason(driver elelem.Driver) elelem.Driver {
	return finishReasonDriver{Driver: driver}
}

func (d finishReasonDriver) Stream(
	ctx context.Context,
	request elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	usage, err := d.Driver.Stream(ctx, request, onDelta)
	if err != nil {
		return usage, ctxerrors.Wrap(err, "stream provider response")
	}

	if usage.FinishReason != elelem.FinishReasonUnset {
		return usage, nil
	}

	runtimecontext.Logger(ctx).Warn(
		"provider stream ended without a finish reason",
		"reason", reasonProviderStreamCut,
		"model", request.Model.ID,
	)

	return usage, ctxerrors.Wrap(
		io.ErrUnexpectedEOF,
		"provider stream ended without a finish reason",
	)
}
