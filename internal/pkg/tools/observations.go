package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
)

type ObservedPath struct {
	Path     string `json:"path"`
	Hash     string `json:"hash,omitempty"`
	HasHash  bool   `json:"hasHash"`
	Revision uint64 `json:"revision,omitempty"`
}

// SnapshotObservations returns independent read state in stable path order.
func (e *Executor) SnapshotObservations() []ObservedPath {
	e.observedMutex.RLock()
	defer e.observedMutex.RUnlock()

	result := make([]ObservedPath, 0, len(e.observed))
	for path, value := range e.observed {
		result = append(result, ObservedPath{
			Path: path, Hash: value.hash, HasHash: value.hasHash,
			Revision: value.revision,
		})
	}

	slices.SortFunc(result, func(left, right ObservedPath) int {
		return strings.Compare(left.Path, right.Path)
	})

	return result
}

// RestoreObservations replaces the ledger after validating the whole snapshot.
// It grants no new file access and preserves stale-content checks on mutation.
func (e *Executor) RestoreObservations(snapshot []ObservedPath) error {
	result := make(map[string]observation, len(snapshot))

	var revision uint64

	for _, entry := range snapshot {
		if err := e.validateObservedPath(entry); err != nil {
			return ctxerrors.Wrap(err, "validate restored observation")
		}

		if _, exists := result[entry.Path]; exists {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"duplicate restored observation")
		}

		result[entry.Path] = observation{
			hash: entry.Hash, hasHash: entry.HasHash,
			revision: entry.Revision,
		}
		revision = max(revision, entry.Revision)
	}

	e.observedMutex.Lock()
	defer e.observedMutex.Unlock()

	e.observed = result
	e.observationRevision = revision

	return nil
}

func (e *Executor) validateObservedPath(entry ObservedPath) error {
	if entry.Path == "" || filepath.Clean(entry.Path) != entry.Path {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"restored observation path")
	}

	if e.virtual {
		if _, err := workspacefs.ValidPath(entry.Path); err != nil {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"restored virtual observation path")
		}
	} else if !filepath.IsAbs(entry.Path) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"restored observation must be absolute")
	}

	return validateObservationHash(entry)
}

func validateObservationHash(entry ObservedPath) error {
	if !entry.HasHash {
		if entry.Hash != "" {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"unread observation has a content hash")
		}

		return nil
	}

	decoded, err := hex.DecodeString(entry.Hash)
	if err != nil || len(decoded) != sha256.Size ||
		hex.EncodeToString(decoded) != entry.Hash {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"restored observation content hash")
	}

	return nil
}
