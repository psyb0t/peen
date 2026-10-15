package session

import (
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

func newTurnLease(sessionID, turnID uuid.UUID) (Lease, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return Lease{}, ctxerrors.Wrap(err, "create turn lease identity")
	}

	return Lease{ID: id, SessionID: sessionID, TurnID: turnID}, nil
}

// holdLease fences transcript writes until their transaction has committed.
func (s *Store) holdLease(lease Lease) (func(), error) {
	s.activeMu.Lock()
	if !s.hasLeaseLocked(lease) {
		s.activeMu.Unlock()

		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"inactive turn lease")
	}

	return s.activeMu.Unlock, nil
}

func (s *Store) hasLeaseLocked(lease Lease) bool {
	active, ok := s.active[lease.SessionID]

	return ok && lease.ID != uuid.Nil && active.turnID == lease.TurnID &&
		active.leaseID == lease.ID
}
