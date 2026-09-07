package tools

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	homePrefix    = "~"
	homePathStart = "~/"
)

// Executor runs one turn's host filesystem and command tools. It resolves
// paths against the turn workspace, records what the turn has observed, and
// serializes mutations so a stale-write check cannot race another tool call.
//
// The workspace is a default directory, never a boundary. Paths outside it,
// including `..` segments, dotfiles, and symlink targets, are resolved and
// used. Operating-system ownership and mode bits are the only restriction.
type Executor struct {
	workspace string
	home      string
	limits    Limits

	mutation sync.Mutex

	observedMutex sync.RWMutex
	observed      map[string]observation
}

type observation struct {
	hash    string
	hasHash bool
}

// NewExecutor validates the turn workspace and bounds. The workspace must be
// an existing absolute directory; the runtime canonicalizes it beforehand.
func NewExecutor(options Options) (*Executor, error) {
	if !filepath.IsAbs(options.Workspace) {
		return nil, ctxerrors.Wrap(
			ErrInvalidOptions,
			"workspace must be absolute",
		)
	}

	info, err := os.Stat(options.Workspace)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "stat tool workspace")
	}

	if !info.IsDir() {
		return nil, ctxerrors.Wrap(ErrNotDirectory, "tool workspace")
	}

	limits := options.Limits.withDefaults()
	if err := limits.validate(); err != nil {
		return nil, ctxerrors.Wrap(err, "validate tool limits")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	return &Executor{
		workspace: filepath.Clean(options.Workspace),
		home:      home,
		limits:    limits,
		observed:  map[string]observation{},
	}, nil
}

// Workspace reports the default directory for this turn's tool calls.
func (e *Executor) Workspace() string {
	return e.workspace
}

// Limits reports the bounds applied to every result.
func (e *Executor) Limits() Limits {
	return e.limits
}

// ResolvePath reports the normalized host path a tool argument refers to.
// It does not observe, create, or otherwise touch the path.
func (e *Executor) ResolvePath(raw string) (string, error) {
	return e.resolvePath(raw)
}

// resolvePath maps a tool path onto the host filesystem. An empty path is the
// workspace, `~` expands to the running user's home, a relative path resolves
// from the workspace, and an absolute path is used as given.
func (e *Executor) resolvePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return e.workspace, nil
	}

	if trimmed == homePrefix || strings.HasPrefix(trimmed, homePathStart) {
		if e.home == "" {
			return "", ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"home directory is unavailable",
			)
		}

		trimmed = filepath.Join(e.home, strings.TrimPrefix(trimmed, homePrefix))
	}

	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed), nil
	}

	return filepath.Join(e.workspace, trimmed), nil
}

// observe records that the turn has seen this path.
func (e *Executor) observe(path string) {
	e.record(path, observation{})
}

// observeContent records a path together with the content hash that was read.
func (e *Executor) observeContent(path, hash string) {
	e.record(path, observation{hash: hash, hasHash: true})
}

func (e *Executor) record(path string, seen observation) {
	key := observationKey(path)

	e.observedMutex.Lock()
	defer e.observedMutex.Unlock()

	existing, ok := e.observed[key]
	if ok && existing.hasHash && !seen.hasHash {
		return
	}

	e.observed[key] = seen
}

// Observed reports whether this turn has already seen the resolved path.
func (e *Executor) Observed(path string) bool {
	key := observationKey(path)

	e.observedMutex.RLock()
	defer e.observedMutex.RUnlock()

	_, ok := e.observed[key]

	return ok
}

func (e *Executor) requireObserved(path string) error {
	if e.Observed(path) {
		return nil
	}

	return ctxerrors.Wrap(ErrNotObserved, "read the path before changing it")
}

func (e *Executor) requireObservedContent(path string, content []byte) error {
	key := observationKey(path)

	e.observedMutex.RLock()
	seen, ok := e.observed[key]
	e.observedMutex.RUnlock()

	if !ok {
		return ctxerrors.Wrap(
			ErrNotObserved,
			"read the file before patching it",
		)
	}

	if !seen.hasHash {
		return ctxerrors.Wrap(
			ErrHashRequired,
			"read the file contents before patching it",
		)
	}

	if seen.hash != hashBytes(content) {
		return ctxerrors.Wrap(ErrStaleHash, path)
	}

	return nil
}

// observationKey collapses symlinked spellings of one path onto a single
// ledger entry so a read through a link satisfies a later mutation.
func observationKey(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}

	return resolved
}
