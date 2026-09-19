package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// maxFrameBytes bounds one frame. A durable page or a tool result is the
// largest thing that crosses this socket, and a frame beyond this is a bug or
// an attempt to exhaust the reader rather than real work.
const maxFrameBytes = 32 << 20

// Conn is one multiplexed protocol connection.
//
// Both sides send and receive on the same socket, so each runs one read loop
// that answers inbound frames and completes outbound ones. A call and a
// command can therefore be in flight at once, which is what lets a controller
// cancel a turn while the worker is still asking for durable records.
type Conn struct {
	socket  net.Conn
	reader  *bufio.Reader
	handler Handler

	writeMutex sync.Mutex

	pendingMutex sync.Mutex
	pending      map[string]chan Frame

	closeOnce sync.Once
	closed    chan struct{}
}

// Handler answers the frames one side receives. The controller implements the
// call side, the worker implements the command side, and each returns a nil
// payload for a frame kind it does not serve.
type Handler interface {
	// HandleCall answers a worker's durable request.
	HandleCall(
		ctx context.Context,
		method string,
		payload json.RawMessage,
	) (any, error)

	// HandleCommand answers a controller's command.
	HandleCommand(
		ctx context.Context,
		command Command,
		payload json.RawMessage,
	) (any, error)
}

// NewConn wraps one socket. The caller starts the read loop with Serve.
func NewConn(socket net.Conn, handler Handler) *Conn {
	return &Conn{
		socket:  socket,
		reader:  bufio.NewReaderSize(socket, bufio.MaxScanTokenSize),
		handler: handler,
		pending: map[string]chan Frame{},
		closed:  make(chan struct{}),
	}
}

// Send writes one frame. Writes are serialized because two goroutines may
// answer a command and issue a call at the same moment.
func (c *Conn) Send(frame Frame) error {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return ctxerrors.Wrap(err, "encode protocol frame")
	}

	if len(encoded) > maxFrameBytes {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"protocol frame of %d bytes exceeds the limit",
			len(encoded),
		)
	}

	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()

	if _, err := c.socket.Write(append(encoded, '\n')); err != nil {
		return ctxerrors.Wrap(err, "write protocol frame")
	}

	return nil
}

// Receive reads exactly one frame. Registration uses it directly, before the
// read loop starts.
func (c *Conn) Receive() (Frame, error) {
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		if len(line) == 0 {
			return Frame{}, ctxerrors.Wrap(err, "read protocol frame")
		}
	}

	if len(line) > maxFrameBytes {
		return Frame{}, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"protocol frame of %d bytes exceeds the limit",
			len(line),
		)
	}

	frame := Frame{}
	if err := json.Unmarshal(line, &frame); err != nil {
		return Frame{}, ctxerrors.Wrap(err, "decode protocol frame")
	}

	return frame, nil
}

// Call issues one request and waits for its answer.
func (c *Conn) Call(
	ctx context.Context,
	kind Kind,
	method string,
	params any,
) (json.RawMessage, error) {
	payload, err := encodePayload(params)
	if err != nil {
		return nil, err
	}

	id := uuid.NewString()
	answer := make(chan Frame, 1)

	c.pendingMutex.Lock()
	c.pending[id] = answer
	c.pendingMutex.Unlock()

	defer func() {
		c.pendingMutex.Lock()
		delete(c.pending, id)
		c.pendingMutex.Unlock()
	}()

	if err := c.Send(Frame{
		Kind:    kind,
		ID:      id,
		Method:  method,
		Payload: payload,
	}); err != nil {
		return nil, err
	}

	select {
	case frame := <-answer:
		if frame.Error != nil {
			return nil, frame.Error.Unwrap()
		}

		return frame.Payload, nil
	case <-c.closed:
		return nil, ctxerrors.Wrapf(
			commerr.ErrFetchFailed,
			"worker protocol closed while %q was in flight",
			method,
		)
	case <-ctx.Done():
		return nil, ctxerrors.Wrapf(ctx.Err(), "await %q", method)
	}
}

// Serve runs the read loop until the socket ends or the context is cancelled.
//
// It returns nil on a clean peer close, because a worker exiting after its
// final transition is the normal end of a connection, not a failure.
func (c *Conn) Serve(ctx context.Context) error {
	defer c.Close()

	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-c.closed:
		}
	}()

	for {
		frame, err := c.Receive()
		if err != nil {
			if isClosed(err) || ctx.Err() != nil {
				return nil
			}

			return err
		}

		c.dispatch(ctx, frame)
	}
}

// dispatch routes one inbound frame. Answers complete a waiter inline, while
// requests run in their own goroutine so a slow durable write cannot stall the
// cancellation frame behind it.
func (c *Conn) dispatch(ctx context.Context, frame Frame) {
	switch frame.Kind {
	case KindResult, KindCommandResult, KindHelloAck:
		c.complete(frame)
	case KindCall, KindCommand:
		go c.answer(ctx, frame)
	case KindHello:
		c.replyError(frame, ctxerrors.Wrap(
			commerr.ErrConflict,
			"worker is already registered",
		))
	}
}

func (c *Conn) complete(frame Frame) {
	c.pendingMutex.Lock()
	answer, found := c.pending[frame.ID]
	c.pendingMutex.Unlock()

	if !found {
		return
	}

	select {
	case answer <- frame:
	default:
	}
}

// answer serves one inbound request and replies. A panic in a handler is
// turned into a failed result rather than taking the whole controller down
// with one bad worker frame.
func (c *Conn) answer(ctx context.Context, frame Frame) {
	defer func() {
		if recovered := recover(); recovered != nil {
			c.replyError(frame, ctxerrors.Wrapf(
				commerr.ErrExecFailed,
				"worker protocol handler panicked: %v",
				recovered,
			))
		}
	}()

	result, err := c.invoke(ctx, frame)
	if err != nil {
		c.replyError(frame, err)

		return
	}

	payload, err := encodePayload(result)
	if err != nil {
		c.replyError(frame, err)

		return
	}

	if err := c.Send(Frame{
		Kind:    answerKind(frame.Kind),
		ID:      frame.ID,
		Payload: payload,
	}); err != nil {
		c.Close()
	}
}

func (c *Conn) invoke(ctx context.Context, frame Frame) (any, error) {
	if frame.Kind == KindCall {
		result, err := c.handler.HandleCall(ctx, frame.Method, frame.Payload)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "serve call %q", frame.Method)
		}

		return result, nil
	}

	result, err := c.handler.HandleCommand(
		ctx,
		Command(frame.Method),
		frame.Payload,
	)
	if err != nil {
		return nil, ctxerrors.Wrapf(err, "serve command %q", frame.Method)
	}

	return result, nil
}

func (c *Conn) replyError(frame Frame, err error) {
	if sendErr := c.Send(Frame{
		Kind:  answerKind(frame.Kind),
		ID:    frame.ID,
		Error: NewError(err),
	}); sendErr != nil {
		c.Close()
	}
}

// Close ends the connection once. Closing an already-closed connection is not
// an error, because both the read loop and its owner may reach it.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)

		if err := c.socket.Close(); err != nil && !isClosed(err) {
			return
		}
	})
}

// Done closes when the connection ends.
func (c *Conn) Done() <-chan struct{} {
	return c.closed
}

func answerKind(request Kind) Kind {
	if request == KindCommand {
		return KindCommandResult
	}

	return KindResult
}

func encodePayload(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode protocol payload")
	}

	return encoded, nil
}

// isClosed reports the ordinary end of a connection, which is not a failure to
// report.
func isClosed(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}
