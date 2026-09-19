package protocol

import (
	"context"
	"encoding/json"
	"net"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

// Connection is a registered worker's side of the protocol.
type Connection struct {
	conn  *Conn
	store *Store
	Ack   HelloAck
}

// Dial connects one worker to its controller and registers it.
//
// The handler answers controller commands. Registration is the first thing on
// the wire and the only place the credential appears; a refusal means this
// worker must exit rather than retry, because its controller will not accept
// the launch it was given.
func Dial(
	ctx context.Context,
	document worker.LaunchDocument,
	handler Handler,
) (*Connection, error) {
	if err := document.Validate(); err != nil {
		return nil, ctxerrors.Wrap(err, "validate the worker launch document")
	}

	dialer := net.Dialer{}

	socket, err := dialer.DialContext(ctx, "unix", document.SocketPath)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "dial the controller worker socket")
	}

	conn := NewConn(socket, handler)

	ack, err := register(conn, document)
	if err != nil {
		conn.Close()

		return nil, err
	}

	return &Connection{
		conn:  conn,
		store: NewStore(conn, document.SessionID, document.GenerationID),
		Ack:   ack,
	}, nil
}

func register(
	conn *Conn,
	document worker.LaunchDocument,
) (HelloAck, error) {
	payload, err := json.Marshal(Hello{
		SessionID:    document.SessionID,
		GenerationID: document.GenerationID,
		Credential:   document.Credential,
		Profile:      document.Profile,
	})
	if err != nil {
		return HelloAck{}, ctxerrors.Wrap(err, "encode the worker hello")
	}

	if err := conn.Send(Frame{
		Kind:    KindHello,
		Payload: payload,
	}); err != nil {
		return HelloAck{}, err
	}

	frame, err := conn.Receive()
	if err != nil {
		return HelloAck{}, ctxerrors.Wrap(
			err,
			"read the controller registration answer",
		)
	}

	if frame.Kind != KindHelloAck {
		return HelloAck{}, ctxerrors.Wrapf(
			ErrWorkerRejected,
			"the controller answered registration with %q",
			frame.Kind,
		)
	}

	if frame.Error != nil {
		return HelloAck{}, ctxerrors.Wrap(
			ErrWorkerRejected,
			frame.Error.Message,
		)
	}

	ack := HelloAck{}
	if err := json.Unmarshal(frame.Payload, &ack); err != nil {
		return HelloAck{}, ctxerrors.Wrap(
			err,
			"decode the controller registration answer",
		)
	}

	return ack, nil
}

// Store is the worker's durable surface over this connection.
func (c *Connection) Store() *Store {
	return c.store
}

// Serve runs the worker's read loop until the controller disconnects or the
// context ends.
func (c *Connection) Serve(ctx context.Context) error {
	return c.conn.Serve(ctx)
}

// Close ends the connection.
func (c *Connection) Close() {
	c.conn.Close()
}

// Done closes when the connection ends.
func (c *Connection) Done() <-chan struct{} {
	return c.conn.Done()
}
