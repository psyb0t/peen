package session

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// IdleCallbackFunc notifies that sessionID's turn ended in state and the
// session now holds no active turn in this store. Store calls it once a
// normal turn end has released the lease, never while holding activeMu, so
// the callback can read IsActive and start a new turn without deadlocking
// the store that just finished writing.
type IdleCallbackFunc func(sessionID uuid.UUID, state models.TurnState)

// IdleNotifier is the optional seam a runtime uses to re-check a session's
// pending notices once its turn ends on its own. A notice published while
// that turn was still running degrades to a queued delivery because the
// session reports busy; nothing else revisits it once the turn is gone.
//
// Not every Storage a caller supplies needs one: a worker's protocol store
// mirrors a lease for cancellation only, and the controller's store it
// forwards to is the one that owns this decision.
type IdleNotifier interface {
	SetIdleCallback(IdleCallbackFunc)
}

var _ IdleNotifier = (*Store)(nil)

// SetIdleCallback installs the post-finalize idle notifier, safe for
// concurrent use. A nil callback disables notification.
func (s *Store) SetIdleCallback(callback IdleCallbackFunc) {
	s.idleCallbackMu.Lock()
	defer s.idleCallbackMu.Unlock()

	s.idleCallback = callback
}

func (s *Store) getIdleCallback() IdleCallbackFunc {
	s.idleCallbackMu.RLock()
	defer s.idleCallbackMu.RUnlock()

	return s.idleCallback
}

// notifyIdle calls the installed idle callback, if any. A caller fires this
// only after releasing the turn's lease and outside activeMu.
func (s *Store) notifyIdle(sessionID uuid.UUID, state models.TurnState) {
	callback := s.getIdleCallback()
	if callback == nil {
		return
	}

	callback(sessionID, state)
}
