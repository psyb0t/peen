package events

import "github.com/google/uuid"

// Discard acknowledges IDs while keeping notices that arrived later.
func (b *Bus) Discard(sessionID uuid.UUID, ids []uuid.UUID) {
	if len(ids) == 0 {
		return
	}

	b.mutex.Lock()
	defer b.mutex.Unlock()

	pending := b.pending[sessionID]
	if pending == nil {
		return
	}

	selected := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		selected[id] = struct{}{}
	}

	kept := make([]Notice, 0, len(pending.notices))
	for _, notice := range pending.notices {
		if _, discard := selected[notice.ID]; discard {
			continue
		}

		kept = append(kept, notice)
	}

	if len(kept) == 0 {
		delete(b.pending, sessionID)

		return
	}

	pending.notices = kept
}
