package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
)

// CancelPending invalidates current admissions, preserving future tickets.
// It cancels bound preparation contexts and leaves other sessions untouched.
func (g *AdmissionGate) CancelPending(sessionID uuid.UUID) int {
	g.mutex.Lock()
	line := g.lines[sessionID]
	retained := line[:0]
	count := 0

	cancellations := make([]context.CancelFunc, 0, len(line))
	for _, ticket := range line {
		select {
		case <-ticket.stopped:
		default:
			close(ticket.stopped)

			count++
		}

		cancellations = append(cancellations, ticket.cancellations...)

		ticket.cancellations = nil
		if ticket.preparing {
			retained = append(retained, ticket)
		}
	}

	clear(line[len(retained):])

	if len(retained) == 0 {
		delete(g.lines, sessionID)
	} else {
		g.lines[sessionID] = retained
	}
	g.mutex.Unlock()

	for _, cancel := range cancellations {
		cancel()
	}

	return count
}

// BindContext keeps preparation cancellable until this ticket leaves admission.
// Releasing admission preserves the context until the caller cancels it.
func (t *AdmissionTicket) BindContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	bound, cancel := context.WithCancel(ctx)

	t.gate.mutex.Lock()
	select {
	case <-t.stopped:
		t.gate.mutex.Unlock()
		cancel()
	default:
		t.preparing = true
		t.cancellations = append(t.cancellations, cancel)
		t.gate.mutex.Unlock()
	}

	return bound, cancel
}

func (t *AdmissionTicket) checkCancellation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "admission context ended")
	}

	select {
	case <-t.stopped:
		return ctxerrors.Wrap(context.Canceled, "session admission stopped")
	default:
		return nil
	}
}
