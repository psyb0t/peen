package protocol

import "github.com/psyb0t/peen/internal/pkg/session"

func (s *Store) rememberTurnLease(lease session.Lease) {
	s.activeMutex.Lock()

	previous, exists := s.active[lease.SessionID]
	if exists && previous.turnID == lease.TurnID &&
		previous.leaseID == lease.ID {
		s.activeMutex.Unlock()

		return
	}

	s.active[lease.SessionID] = activeTurn{
		turnID: lease.TurnID, leaseID: lease.ID,
	}
	s.activeMutex.Unlock()

	if previous.cancel != nil {
		previous.cancel()
	}
}
