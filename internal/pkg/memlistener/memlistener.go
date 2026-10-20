// Package memlistener is a net.Listener with no network address.
//
// A connection exists only when code in the same process dials it, so a server
// that accepts on one is unreachable from any socket, port, or other process.
// The one-shot command serves its controller's API this way.
package memlistener

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/psyb0t/ctxerrors"
)

// network names the listener's address family. It is not a real network, so
// no dialer outside this package can reach it.
const network = "memory"

// Listener hands the server end of each dialed connection to Accept.
type Listener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
}

// New builds an open listener.
func New() *Listener {
	return &Listener{
		connections: make(chan net.Conn),
		closed:      make(chan struct{}),
	}
}

// Accept waits for the next dialed connection. It returns net.ErrClosed once
// the listener is closed, which an HTTP server treats as a clean stop.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, ctxerrors.Wrap(
			net.ErrClosed,
			"accept an in-memory connection",
		)
	}
}

// Close stops Accept and refuses later dials. Connections already accepted
// stay open until their owners close them. Closing twice is a no-op.
func (l *Listener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })

	return nil
}

// Addr reports the listener's placeholder address.
func (l *Listener) Addr() net.Addr {
	return address{}
}

// DialContext connects to the listener. It has the signature of
// net.Dialer.DialContext, so an http.Transport or a WebSocket dialer can use it
// as is. The network and address are ignored: there is one place to connect.
func (l *Listener) DialContext(
	ctx context.Context,
	_, _ string,
) (net.Conn, error) {
	server, client := net.Pipe()

	select {
	case l.connections <- server:
		return client, nil
	case <-l.closed:
		return nil, closePipe(
			ctxerrors.Wrap(net.ErrClosed, "dial a closed in-memory listener"),
			server,
			client,
		)
	case <-ctx.Done():
		return nil, closePipe(
			ctxerrors.Wrap(ctx.Err(), "dial the in-memory listener"),
			server,
			client,
		)
	}
}

// closePipe closes both ends of a pipe nobody accepted and joins any close
// failure to the dial error.
func closePipe(dialErr error, server, client net.Conn) error {
	return errors.Join(dialErr, server.Close(), client.Close())
}

type address struct{}

func (address) Network() string { return network }

func (address) String() string { return network }
