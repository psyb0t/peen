package tools

import "errors"

var (
	// ErrInvalidOptions reports unusable executor construction options.
	ErrInvalidOptions = errors.New("invalid tool executor options")
	// ErrInvalidLimits reports a limit set that cannot bound tool work.
	ErrInvalidLimits = errors.New("invalid tool limits")
	// ErrNotDirectory reports a path that exists but is not a directory.
	ErrNotDirectory = errors.New("path is not a directory")
	// ErrNotRegularFile reports a path that is not a regular file.
	ErrNotRegularFile = errors.New("path is not a regular file")
	// ErrBinaryContent reports a text operation refused on binary content.
	ErrBinaryContent = errors.New("binary content is not supported")
	// ErrNotObserved reports a mutation on a path never read in this turn.
	ErrNotObserved = errors.New("path was not observed in the current turn")
	// ErrStaleHash reports a mismatch between expected and actual content.
	ErrStaleHash = errors.New("expected content hash does not match")
	// ErrHashRequired reports a mutation missing its expected content hash.
	ErrHashRequired = errors.New("expected content hash is required")
	// ErrMatchNotFound reports edit text absent from the file.
	ErrMatchNotFound = errors.New("exact text was not found")
	// ErrMatchNotUnique reports edit text present more than once.
	ErrMatchNotUnique = errors.New("exact text matched more than once")
	// ErrOverlappingEdits reports edits that replace the same region.
	ErrOverlappingEdits = errors.New("edits overlap")
	// ErrDirectoryNotEmpty reports a non-recursive removal of a full tree.
	ErrDirectoryNotEmpty = errors.New("directory is not empty")
	// ErrLimitExceeded reports input too large to bound by truncation.
	ErrLimitExceeded = errors.New("tool limit exceeded")
	// ErrCommandStartFailed reports a command that could not be launched.
	ErrCommandStartFailed = errors.New("command could not be started")
)
