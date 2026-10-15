package server

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

const webSocketPresenceStopTimeout = 10 * time.Second

type webSocketPresenceWriter struct {
	http.ResponseWriter
	attach func() func()
}

type webSocketSessionPresence struct {
	gate       sync.Mutex
	clients    map[uuid.UUID]struct{}
	references int
}

func (s *Server) trackWebSocketPresence(
	ctx context.Context,
	w http.ResponseWriter,
	clientID, sessionID uuid.UUID,
) *webSocketPresenceWriter {
	return &webSocketPresenceWriter{
		ResponseWriter: w,
		attach: func() func() {
			return s.attachWebSocketPresence(ctx, clientID, sessionID)
		},
	}
}

func (w *webSocketPresenceWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *webSocketPresenceWriter) Hijack() (
	net.Conn, *bufio.ReadWriter, error,
) {
	connection, buffered, err := http.NewResponseController(w.ResponseWriter).
		Hijack()
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "hijack session websocket")
	}

	return &webSocketPresenceConnection{
		Conn: connection, detached: w.attach(),
	}, buffered, nil
}

type webSocketPresenceConnection struct {
	net.Conn
	closeOnce sync.Once
	closeErr  error
	notified  atomic.Bool
	detached  func()
}

func (c *webSocketPresenceConnection) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.Conn.Close() })

	if c.notified.CompareAndSwap(false, true) {
		c.detached()
	}

	if c.closeErr != nil {
		return ctxerrors.Wrap(c.closeErr, "close session websocket")
	}

	return nil
}

func (s *Server) attachWebSocketPresence(
	ctx context.Context,
	clientID, sessionID uuid.UUID,
) func() {
	s.webSocketPresenceMutex.Lock()

	presence := s.webSocketPresence[sessionID]
	if presence == nil {
		presence = &webSocketSessionPresence{
			clients: make(map[uuid.UUID]struct{}),
		}
		s.webSocketPresence[sessionID] = presence
	}

	presence.references++
	s.webSocketPresenceMutex.Unlock()

	presence.gate.Lock()
	presence.clients[clientID] = struct{}{}
	presence.gate.Unlock()

	return sync.OnceFunc(func() {
		s.detachWebSocketPresence(ctx, clientID, sessionID, presence)
	})
}

func (s *Server) detachWebSocketPresence(
	ctx context.Context,
	clientID, sessionID uuid.UUID,
	presence *webSocketSessionPresence,
) {
	presence.gate.Lock()
	delete(presence.clients, clientID)

	if len(presence.clients) == 0 {
		s.stopLastWebSocketClient(ctx, sessionID)
	}
	presence.gate.Unlock()

	s.webSocketPresenceMutex.Lock()
	defer s.webSocketPresenceMutex.Unlock()

	presence.references--
	if presence.references == 0 {
		delete(s.webSocketPresence, sessionID)
	}
}

func (s *Server) stopLastWebSocketClient(
	ctx context.Context,
	sessionID uuid.UUID,
) {
	ctx, cancel := context.WithTimeout(ctx,
		webSocketPresenceStopTimeout)
	defer cancel()

	ctx = ctxscope.Set(ctx, ctxscope.Attr("session_id", sessionID.String()))

	// Keep reconnect admission behind the durable stop and worker interrupt.
	if err := s.stopUnattendedSession(ctx, sessionID); err != nil {
		ctxscope.GetLogger(ctx).Error("unattended session stop failed",
			"err", err)

		return
	}

	ctxscope.GetLogger(ctx).Info("last session client disconnected")
}

func (s *Server) stopUnattendedSession(
	ctx context.Context,
	sessionID uuid.UUID,
) error {
	var stopErr error
	if _, err := s.deps.Runtime.CancelSession(ctx, sessionID); err != nil {
		stopErr = ctxerrors.Wrap(err, "record unattended session stop")
	}

	if _, err := s.deps.Turns.CancelSessionTurn(ctx, sessionID); err != nil {
		stopErr = errors.Join(stopErr,
			ctxerrors.Wrap(err, "interrupt unattended session worker"))
	}

	return stopErr
}
