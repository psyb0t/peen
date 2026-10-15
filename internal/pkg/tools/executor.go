package tools

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
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
	filesystem         workspacefs.FS
	virtual            bool
	workspace          string
	protectedDirectory string
	home               string
	limits             Limits

	// environment is the base environment of every command. Nil means the
	// process environment.
	environment []string

	mutation *sync.Mutex

	observedMutex       sync.RWMutex
	observed            map[string]observation
	observationRevision uint64
}

type observation struct {
	hash     string
	hasHash  bool
	revision uint64
}

// NewExecutor validates the turn workspace and bounds. The workspace must be
// an existing absolute directory; the runtime canonicalizes it beforehand.
func NewExecutor(options Options) (*Executor, error) {
	if !options.Virtual && !filepath.IsAbs(options.Workspace) {
		return nil, ctxerrors.Wrap(
			ErrInvalidOptions,
			"workspace must be absolute",
		)
	}

	filesystem := options.Filesystem
	if filesystem == nil {
		filesystem = workspacefs.Host{}
	}

	info, err := filesystem.Stat(options.Workspace)
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

	protectedDirectory := options.ProtectedDirectory
	if protectedDirectory == "" {
		protectedDirectory = options.Workspace
	}

	return &Executor{
		filesystem:         filesystem,
		virtual:            options.Virtual,
		workspace:          filepath.Clean(options.Workspace),
		protectedDirectory: filepath.Clean(protectedDirectory),
		mutation:           &sync.Mutex{},
		home:               home,
		limits:             limits,
		observed:           map[string]observation{},

		environment: slices.Clone(options.Environment),
	}, nil
}

// commandBaseEnvironment is the environment a command starts from, before the
// request's own additions.
func (e *Executor) commandBaseEnvironment() []string {
	if e.environment == nil {
		return os.Environ()
	}

	return slices.Clone(e.environment)
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
	if e.virtual {
		if trimmed == "" {
			return e.workspace, nil
		}

		if filepath.IsAbs(trimmed) {
			return "", ctxerrors.Wrap(
				ErrInvalidOptions,
				"absolute virtual path",
			)
		}

		name := filepath.ToSlash(filepath.Join(e.workspace, trimmed))

		resolved, err := workspacefs.ValidPath(name)
		if err != nil {
			return "", ctxerrors.Wrap(err, "resolve virtual tool path")
		}

		return resolved, nil
	}

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
	key := e.observationKey(path)

	e.observedMutex.Lock()
	defer e.observedMutex.Unlock()

	existing, ok := e.observed[key]
	if ok && existing.hasHash && !seen.hasHash {
		return
	}

	if e.observationRevision < math.MaxUint64 {
		e.observationRevision++
	}

	seen.revision = e.observationRevision
	e.observed[key] = seen
}

// Observed reports whether this turn has already seen the resolved path.
func (e *Executor) Observed(path string) bool {
	key := e.observationKey(path)

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
	key := e.observationKey(path)

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
func (e *Executor) observationKey(path string) string {
	resolved, err := e.filesystem.RealPath(path)
	if err != nil {
		return path
	}

	return resolved
}
