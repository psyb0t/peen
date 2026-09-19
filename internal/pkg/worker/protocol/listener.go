package protocol

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	// socketDirectoryMode keeps the worker socket root private to the
	// controller's own account. A socket another account could connect to
	// would make the credential the only barrier.
	socketDirectoryMode os.FileMode = 0o700

	// socketMode keeps one session's socket private for the same reason.
	socketMode os.FileMode = 0o600
)

// Registrar verifies one worker's registration.
//
// The supervisor implements it against the durable generation record, so a
// worker is accepted only when the controller itself recorded that session,
// that generation, and that credential hash.
type Registrar interface {
	AcceptWorker(ctx context.Context, hello Hello) (HelloAck, error)
}

// Listener is one session's private worker socket.
//
// It accepts exactly the worker its controller launched. A second connection
// on the same socket is refused while one is live, because one generation is
// one process.
type Listener struct {
	socketPath string
	sessionID  uuid.UUID
	registrar  Registrar
	store      *session.Store
	publisher  Publisher
	net        net.Listener

	mutex     sync.Mutex
	connected *Worker
}

// Worker is a registered worker as the controller holds it.
type Worker struct {
	conn         *Conn
	SessionID    uuid.UUID
	GenerationID uuid.UUID
}

// Listen opens one session's private socket.
//
// A stale socket file from a controller that died is removed first, because a
// Unix socket is not cleaned up by the kernel and the new controller owns this
// path by construction: it is named for the session and lives under the
// controller's own private root.
func Listen(
	ctx context.Context,
	socketPath string,
	sessionID uuid.UUID,
	registrar Registrar,
	store *session.Store,
	publisher Publisher,
) (*Listener, error) {
	if err := os.MkdirAll(
		filepath.Dir(socketPath),
		socketDirectoryMode,
	); err != nil {
		return nil, ctxerrors.Wrap(err, "create the worker socket root")
	}

	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, ctxerrors.Wrap(err, "remove a stale worker socket")
	}

	config := net.ListenConfig{}

	socket, err := config.Listen(ctx, "unix", socketPath)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "listen on the worker socket")
	}

	if err := os.Chmod(socketPath, socketMode); err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"restrict the worker socket permissions",
		)
	}

	return &Listener{
		socketPath: socketPath,
		sessionID:  sessionID,
		registrar:  registrar,
		store:      store,
		publisher:  publisher,
		net:        socket,
	}, nil
}

// Path is the socket a worker launch document points at.
func (l *Listener) Path() string {
	return l.socketPath
}

// Serve accepts worker connections until the context ends or the listener is
// closed.
func (l *Listener) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		l.Close()
	}()

	for {
		socket, err := l.net.Accept()
		if err != nil {
			// A closed listener is the ordinary end of serving, not a
			// failure to report.
			//nolint:nilerr // A closed listener ends serving cleanly.
			if ctx.Err() != nil || isClosed(err) {
				return nil
			}

			return ctxerrors.Wrap(err, "accept a worker connection")
		}

		go l.serveConnection(ctx, socket)
	}
}

// serveConnection registers one worker and serves it until it disconnects.
func (l *Listener) serveConnection(ctx context.Context, socket net.Conn) {
	logger := ctxscope.GetLogger(ctx)

	registered, conn, err := l.register(ctx, socket)
	if err != nil {
		logger.Warn(
			"worker registration refused",
			"reason", "worker_registration_refused",
			"session_id", l.sessionID.String(),
			"err", err,
		)

		if closeErr := socket.Close(); closeErr != nil {
			logger.Debug("closing a refused worker socket failed",
				"err", closeErr)
		}

		return
	}

	defer l.disconnect(registered)

	if err := conn.Serve(ctx); err != nil {
		logger.Warn(
			"worker connection ended with an error",
			"session_id", registered.SessionID.String(),
			"generation_id", registered.GenerationID.String(),
			"err", err,
		)
	}
}

// register reads the hello frame, verifies it, and answers.
func (l *Listener) register(
	ctx context.Context,
	socket net.Conn,
) (*Worker, *Conn, error) {
	conn := NewConn(socket, nil)

	frame, err := conn.Receive()
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "read the worker hello")
	}

	if frame.Kind != KindHello {
		refusal := ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"a worker must register before sending %q",
			frame.Kind,
		)
		l.refuse(conn, refusal)

		return nil, nil, refusal
	}

	hello, err := decode[Hello](frame.Payload)
	if err != nil {
		l.refuse(conn, err)

		return nil, nil, err
	}

	// The socket already binds one session. A hello naming another is a worker
	// reaching for a session it was not launched for. Answering rather than
	// dropping the connection means that worker exits with a reason instead of
	// treating a silent close as a transport fault worth retrying.
	if hello.SessionID != l.sessionID {
		refusal := ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"a worker may only register for the session it was launched for",
		)
		l.refuse(conn, refusal)

		return nil, nil, refusal
	}

	ack, err := l.registrar.AcceptWorker(ctx, hello)
	if err != nil {
		refusal := ctxerrors.Wrap(err, "accept the worker registration")
		l.refuse(conn, refusal)

		return nil, nil, refusal
	}

	registered, err := l.accept(conn, hello, ack)
	if err != nil {
		return nil, nil, err
	}

	return registered, conn, nil
}

// accept installs the per-session handler and confirms registration.
func (l *Listener) accept(
	conn *Conn,
	hello Hello,
	ack HelloAck,
) (*Worker, error) {
	registered := &Worker{
		conn:         conn,
		SessionID:    hello.SessionID,
		GenerationID: hello.GenerationID,
	}

	l.mutex.Lock()

	if l.connected != nil {
		l.mutex.Unlock()

		refusal := ctxerrors.Wrap(
			commerr.ErrConflict,
			"this session already has a live worker",
		)
		l.refuse(conn, refusal)

		return nil, refusal
	}

	l.connected = registered
	l.mutex.Unlock()

	conn.handler = NewController(
		l.store,
		l.publisher,
		hello.SessionID,
		hello.GenerationID,
	)

	payload, err := json.Marshal(ack)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode the worker hello ack")
	}

	if err := conn.Send(Frame{
		Kind:    KindHelloAck,
		Payload: payload,
	}); err != nil {
		return nil, err
	}

	return registered, nil
}

// refuse answers a rejected registration so the worker exits with a reason
// instead of retrying against a controller that will never accept it.
func (l *Listener) refuse(conn *Conn, cause error) {
	if err := conn.Send(Frame{
		Kind:  KindHelloAck,
		Error: NewError(cause),
	}); err != nil {
		conn.Close()
	}
}

func (l *Listener) disconnect(registered *Worker) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	if l.connected == registered {
		l.connected = nil
	}
}

// Connected returns the live worker, if one has registered.
func (l *Listener) Connected() (*Worker, bool) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return l.connected, l.connected != nil
}

// Close stops accepting and removes the socket file.
func (l *Listener) Close() {
	if err := l.net.Close(); err != nil && !isClosed(err) {
		return
	}

	if err := os.Remove(l.socketPath); err != nil && !os.IsNotExist(err) {
		return
	}
}

// RunTurn hands one accepted user message to the worker and waits for its
// answer.
func (w *Worker) RunTurn(
	ctx context.Context,
	request RunTurn,
) (TurnResult, error) {
	payload, err := w.conn.Call(
		ctx,
		KindCommand,
		string(CommandRunTurn),
		request,
	)
	if err != nil {
		return TurnResult{}, ctxerrors.Wrap(err, "run a turn in the worker")
	}

	result := TurnResult{}
	if err := json.Unmarshal(payload, &result); err != nil {
		return TurnResult{}, ctxerrors.Wrap(err, "decode the turn result")
	}

	return result, nil
}

// Cancel asks the worker to cancel its session's active turn.
func (w *Worker) Cancel(ctx context.Context) (bool, error) {
	payload, err := w.conn.Call(
		ctx,
		KindCommand,
		string(CommandCancel),
		nil,
	)
	if err != nil {
		return false, ctxerrors.Wrap(err, "cancel the worker turn")
	}

	result := CancelResult{}
	if err := json.Unmarshal(payload, &result); err != nil {
		return false, ctxerrors.Wrap(err, "decode the cancel result")
	}

	return result.CancelRequested, nil
}

// SignalJob asks the worker to signal one of its running jobs.
//
// A worker that no longer holds the job answers Handled false rather than an
// error, because the job may have exited between the caller's read and this
// command.
func (w *Worker) SignalJob(
	ctx context.Context,
	jobID uuid.UUID,
	signal string,
) (SignalJobResult, error) {
	payload, err := w.conn.Call(
		ctx,
		KindCommand,
		string(CommandSignalJob),
		SignalJob{JobID: jobID, Signal: signal},
	)
	if err != nil {
		return SignalJobResult{}, ctxerrors.Wrap(err, "signal the worker job")
	}

	result := SignalJobResult{}
	if err := json.Unmarshal(payload, &result); err != nil {
		return SignalJobResult{}, ctxerrors.Wrap(
			err,
			"decode the signal_job result",
		)
	}

	return result, nil
}

// Shutdown asks the worker to finish and exit.
func (w *Worker) Shutdown(ctx context.Context) error {
	if _, err := w.conn.Call(
		ctx,
		KindCommand,
		string(CommandShutdown),
		nil,
	); err != nil {
		return ctxerrors.Wrap(err, "ask the worker to shut down")
	}

	return nil
}

// Done closes when the worker's connection ends.
func (w *Worker) Done() <-chan struct{} {
	return w.conn.Done()
}

// Disconnect drops the connection without waiting for the worker.
func (w *Worker) Disconnect() {
	w.conn.Close()
}
