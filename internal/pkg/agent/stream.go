package agent

import (
	"context"
	"errors"
	"io"
	"runtime/debug"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/essessey/elelemstream"
	essesseysse "github.com/psyb0t/essessey/sse"
)

// Chatz-compatible advisory and terminal event names. They are not among
// essessey's seven content-block types because they describe the stream rather
// than the message, so they go through the publisher's generic Publish.
const (
	streamEventChatStatus = "chat_status"
	streamEventError      = "error"

	streamStatusConnecting        = "connecting"
	streamStatusWaitingFirstToken = "waiting_first_token"
	streamStatusStreaming         = "streaming"
	streamStatusRunningTool       = "running_tool"
	streamStatusRetrying          = "retrying"

	// The terminal error classifications and their user-facing text are
	// Chatz's, byte for byte. This is a compatibility contract: a Chatz
	// adapter must be able to reuse its existing stream parser without
	// translating anything, so inventing Peen's own vocabulary here would
	// break the one property the SSE contract exists to provide.
	streamErrorTypeTimeout          = "upstream_timeout"
	streamErrorTypeRateLimited      = "rate_limited"
	streamErrorTypeModelUnavailable = "model_unavailable"
	streamErrorTypeContextLimit     = "context_limit"
	streamErrorTypeRequestFailed    = "request_failed"

	streamErrorMessageTimeout = "The model did not respond in time. Try again."
	//nolint:lll // user-facing terminal message must remain one line
	streamErrorMessageRateLimited = "The model provider is busy. Try again shortly."
	//nolint:lll // user-facing terminal message must remain one line
	streamErrorMessageModelUnavailable = "The selected model is unavailable. Contact an administrator."
	//nolint:lll // user-facing terminal message must remain one line
	streamErrorMessageContextLimit  = "This conversation is too large for the selected model."
	streamErrorMessageRequestFailed = "The model request failed. Try again."
)

// chatStatusPayload repeats the event name in the body because Chatz does. A
// client that reads the frame body alone can still tell what it is.
type chatStatusPayload struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// streamErrorPayload nests the detail under "error", matching Chatz's shape.
type streamErrorPayload struct {
	Type  string            `json:"type"`
	Error streamErrorDetail `json:"error"`
}

type streamErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// streamStatus publishes the advisory progress frames that bracket a stream.
//
// Content blocks are not its business: elelemstream's adapter owns those and
// the block arithmetic that goes with them. This only reports what the turn is
// doing between them.
type streamStatus struct {
	publisher *essessey.Publisher

	// current is the last status published, so a repeat is not resent on every
	// delta of a long answer.
	current string
}

// report advertises whatever progress an event implies, and is a no-op for the
// events that imply none.
func (s *streamStatus) report(eventType string) error {
	return s.publish(statusForEvent(eventType))
}

func (s *streamStatus) publish(status string) error {
	if status == "" || s.current == status {
		return nil
	}

	s.current = status

	if err := s.publisher.Publish(
		streamEventChatStatus,
		chatStatusPayload{Type: streamEventChatStatus, Status: status},
	); err != nil {
		return ctxerrors.Wrap(err, "send SSE chat status")
	}

	return nil
}

// publishTerminalError tells a client why a stream stopped.
//
// Headers are already committed by the time a turn can fail, so the JSON error
// envelope is unreachable and the only alternative is severing the connection
// with no explanation at all. Cancellation publishes nothing: the plan makes it
// a normal end to a stream, not a failure.
func (s *streamStatus) publishTerminalError(streamErr error) error {
	if streamErr == nil || errors.Is(streamErr, context.Canceled) {
		return nil
	}

	if err := s.publisher.Publish(
		streamEventError,
		classifyStreamFailure(streamErr),
	); err != nil {
		return ctxerrors.Wrap(err, "send SSE terminal error")
	}

	return nil
}

// classifyStreamFailure reduces any turn failure to one of Chatz's terminal
// classifications and its fixed user-facing message.
//
// A raw error carries file paths, provider response bodies, and whatever a
// tool printed. None of that crosses the SSE boundary. The classification is
// the part a client can act on; the detail stays in the transcript and the
// operator's logs.
func classifyStreamFailure(streamErr error) streamErrorPayload {
	switch {
	case matchesAnyError(streamErr, context.DeadlineExceeded):
		return newStreamFailure(
			streamErrorTypeTimeout,
			streamErrorMessageTimeout,
		)
	case matchesAnyError(streamErr, commerr.ErrRateLimited):
		return newStreamFailure(
			streamErrorTypeRateLimited,
			streamErrorMessageRateLimited,
		)
	case matchesAnyError(
		streamErr,
		commerr.ErrNotAuthenticated,
		commerr.ErrPermissionDenied,
		commerr.ErrUnavailable,
		ErrModelUnavailable,
	):
		return newStreamFailure(
			streamErrorTypeModelUnavailable,
			streamErrorMessageModelUnavailable,
		)
	case matchesAnyError(
		streamErr,
		elelem.ErrContextExceeded,
		elelem.ErrMaxOutputExceedsContext,
		ErrCompactionUnavailable,
		ErrCompactionInsufficient,
	):
		return newStreamFailure(
			streamErrorTypeContextLimit,
			streamErrorMessageContextLimit,
		)
	default:
		return newStreamFailure(
			streamErrorTypeRequestFailed,
			streamErrorMessageRequestFailed,
		)
	}
}

func matchesAnyError(err error, candidates ...error) bool {
	for _, candidate := range candidates {
		if errors.Is(err, candidate) {
			return true
		}
	}

	return false
}

func newStreamFailure(kind, message string) streamErrorPayload {
	return streamErrorPayload{
		Type: streamEventError,
		Error: streamErrorDetail{
			Type:    kind,
			Message: message,
		},
	}
}

// statusForEvent maps an event to the progress it advertises. An empty result
// means the event changes nothing a client should be told about.
//
// It reads both vocabularies on purpose: content blocks arrive under
// essessey's names from the adapter, while tool and retry activity are Peen's
// own harness records.
func statusForEvent(eventType string) string {
	switch eventType {
	case essessey.EventTypeContentBlockStart,
		essessey.EventTypeContentBlockDelta:
		return streamStatusStreaming
	case EventTypeToolUse:
		return streamStatusRunningTool
	case EventTypeProviderRetry:
		return streamStatusRetrying
	default:
		return ""
	}
}

func (r *Runtime) writeStream(
	ctx context.Context,
	writer *io.PipeWriter,
	prepared *preparedTurn,
) {
	defer recoverStreamPanic(ctx, writer)

	status, err := startStream(ctx, writer, prepared)
	if err != nil {
		// The lease was acquired before this goroutine started, so returning
		// here without finalizing would hold it forever and leave the turn
		// running with nothing driving it. A client that hangs up before the
		// first frame reaches exactly this path.
		closeStreamWriter(
			ctx,
			writer,
			r.finalizeFailedTurn(ctx, prepared, err),
		)

		return
	}

	result, err := r.runLease(ctx, prepared)
	if err != nil {
		// The client already has headers, so this frame is the only way it
		// learns the turn failed rather than the connection simply dying.
		if publishErr := status.publishTerminalError(err); publishErr != nil {
			ctxscope.GetLogger(ctx).Warn(
				"publishing the terminal SSE error failed",
				"err", publishErr,
			)
		}

		closeStreamWriter(
			ctx,
			writer,
			ctxerrors.Wrap(err, "run streamed agent turn"),
		)

		return
	}

	r.finishStream(ctx, writer, prepared.publisher, result)
}

func recoverStreamPanic(ctx context.Context, writer *io.PipeWriter) {
	recovered := recover()
	if recovered == nil {
		return
	}

	ctxscope.GetLogger(ctx).Error(
		"SSE stream goroutine panicked",
		"panic", recovered,
		"stack", string(debug.Stack()),
	)
	closeStreamWriter(ctx, writer, ctxerrors.New("SSE stream panic"))
}

// startStream builds the turn's publisher over the client's writer and the
// transcript, then opens the message.
func startStream(
	ctx context.Context,
	writer *io.PipeWriter,
	prepared *preparedTurn,
) (*streamStatus, error) {
	wire := essesseysse.NewWriterSink(writer)
	prepared.attachPublisher(ctx, wire)

	// Advisory frames get their own publisher, writing only to the wire.
	//
	// Two reasons. They are stream decoration, not conversation, so recording
	// them would put a progress frame between every pair of durable content
	// events. And routing them through the content publisher would re-enter
	// the transcript sink from inside the turn's own event recording, which
	// deadlocks on a mutex that is already held. WriterSink guards its own
	// writes, so two publishers over one wire is safe.
	status := &streamStatus{publisher: essessey.NewPublisher(ctx, wire)}
	prepared.turn.statusReporter = status

	if err := status.publish(streamStatusConnecting); err != nil {
		return nil, err
	}

	if err := prepared.publisher.SendStreamPreamble(
		prepared.turn.requestID.String(),
		prepared.opened.Session.ID.String(),
		prepared.modelReference,
	); err != nil {
		return nil, ctxerrors.Wrap(err, "send SSE preamble")
	}

	if err := status.publish(streamStatusWaitingFirstToken); err != nil {
		return nil, err
	}

	return status, nil
}

// finishStream closes the message. The adapter has already closed whatever
// content block it had open, so there is nothing of Peen's left to close.
func (r *Runtime) finishStream(
	ctx context.Context,
	writer *io.PipeWriter,
	publisher *essessey.Publisher,
	result *TurnResult,
) {
	// The real stop reason and output count, not a hardcoded clean end. A
	// client reading stop_reason has to be able to tell a finished answer from
	// one the provider truncated.
	stopReason := elelemstream.MapStopReason(
		result.FinishReason,
		result.HasToolCalls,
	)

	if err := publisher.SendStreamEpilogue(
		stopReason,
		int(result.OutputTokens),
	); err != nil {
		closeStreamWriter(ctx, writer, ctxerrors.Wrap(err, "send SSE epilogue"))

		return
	}

	ctxscope.GetLogger(ctx).Info(
		"SSE agent stream completed",
		"session_id", result.SessionID.String(),
		"model", result.Model,
	)
	closeStreamWriter(ctx, writer, nil)
}

func closeStreamWriter(
	ctx context.Context,
	writer *io.PipeWriter,
	streamErr error,
) {
	var err error
	if streamErr == nil {
		err = writer.Close()
	} else {
		err = writer.CloseWithError(streamErr)
	}

	if err != nil {
		ctxscope.GetLogger(ctx).Debug(
			"close SSE stream writer failed",
			"err", err,
		)
	}
}
