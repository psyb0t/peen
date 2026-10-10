package session

import (
	"context"
	"slices"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
)

// AdmissionGate admits messages to each session one at a time, in the order
// their places were reserved.
//
// Admitting a message means deciding whether it starts a turn or joins the
// running turn's queue, and making that decision take effect. Two messages for
// one session admitted at once can both see an idle session and race for its
// lease, so the loser is refused as busy even though it only had to queue, and
// the one sent second can start the turn. Holding the gate across the decision
// and its effect removes both races.
//
// Sessions do not share a line, so a slow admission in one never delays
// another. A session's line exists only while a ticket is in it.
type AdmissionGate struct {
	mutex sync.Mutex
	lines map[uuid.UUID][]*AdmissionTicket
}

// AdmissionTicket is one message's place in its session's admission line.
type AdmissionTicket struct {
	gate      *AdmissionGate
	sessionID uuid.UUID

	// admitted is closed when the ticket reaches the front of its line.
	admitted    chan struct{}
	releaseOnce sync.Once
}

// NewAdmissionGate builds a gate with every session's line empty.
func NewAdmissionGate() *AdmissionGate {
	return &AdmissionGate{lines: map[uuid.UUID][]*AdmissionTicket{}}
}

// Reserve takes the next place in the session's line without waiting.
//
// A caller that hands the message to another goroutine reserves before doing
// so, which fixes the admission order to the order the messages were received
// rather than the order those goroutines happen to run in. The ticket must be
// released on every path, admitted or not.
func (g *AdmissionGate) Reserve(sessionID uuid.UUID) *AdmissionTicket {
	ticket := &AdmissionTicket{
		gate:      g,
		sessionID: sessionID,
		admitted:  make(chan struct{}),
	}

	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.lines[sessionID] = append(g.lines[sessionID], ticket)

	if len(g.lines[sessionID]) == 1 {
		close(ticket.admitted)
	}

	return ticket
}

// Acquire reserves a place and waits for it. It returns the ticket's release
// function, which is safe to call more than once, so a caller can release as
// soon as its message is admitted and still defer the call for every failure
// path.
func (g *AdmissionGate) Acquire(
	ctx context.Context,
	sessionID uuid.UUID,
) (func(), error) {
	ticket := g.Reserve(sessionID)

	if err := ticket.Wait(ctx); err != nil {
		ticket.Release()

		return nil, err
	}

	return ticket.Release, nil
}

// SessionID is the session whose line the ticket is in.
func (t *AdmissionTicket) SessionID() uuid.UUID {
	return t.sessionID
}

// Wait blocks until every ticket reserved before this one is released. A
// caller that stops waiting cancels ctx, which returns the context error; the
// ticket still holds its place until it is released.
func (t *AdmissionTicket) Wait(ctx context.Context) error {
	select {
	case <-t.admitted:
		return nil
	case <-ctx.Done():
		return ctxerrors.Wrap(ctx.Err(), "wait to admit a session message")
	}
}

// Release leaves the line. When the ticket was at the front, the next one is
// admitted. Releasing again does nothing.
func (t *AdmissionTicket) Release() {
	t.releaseOnce.Do(func() {
		t.gate.leave(t)
	})
}

func (g *AdmissionGate) leave(ticket *AdmissionTicket) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	line := g.lines[ticket.sessionID]

	position := slices.Index(line, ticket)
	if position < 0 {
		return
	}

	line = slices.Delete(line, position, position+1)
	if len(line) == 0 {
		delete(g.lines, ticket.sessionID)

		return
	}

	g.lines[ticket.sessionID] = line

	if position == 0 {
		close(line[0].admitted)
	}
}
