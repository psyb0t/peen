package tools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

type preparedPatchAction struct {
	operation   PatchOperation
	path        string
	destination string
	oldContent  []byte
	newContent  []byte
	mode        os.FileMode
	diff        string
}

const maxPatchPathsPerAction = 2

// ApplyPatch parses and applies one bounded Codex-compatible patch. Every
// path, observation, destination, and update hunk is checked before the first
// filesystem mutation. Each file write is atomic, but a multi-file patch is
// not globally transactional.
func (e *Executor) ApplyPatch(
	ctx context.Context,
	input ApplyPatchInput,
) (ApplyPatchOutput, error) {
	if err := ctx.Err(); err != nil {
		return ApplyPatchOutput{}, ctxerrors.Wrap(err, "apply patch canceled")
	}

	if len(input.Patch) > e.limits.MaxPatchBytes {
		return ApplyPatchOutput{}, ctxerrors.Wrap(
			ErrLimitExceeded,
			"patch document exceeds byte bound",
		)
	}

	parsed, err := parseApplyPatch(input.Patch)
	if err != nil {
		return ApplyPatchOutput{}, err
	}

	if len(parsed.actions) > e.limits.MaxPatchFiles {
		return ApplyPatchOutput{}, ctxerrors.Wrap(
			ErrLimitExceeded,
			"too many patch files",
		)
	}

	if parsed.hunks > e.limits.MaxPatchHunks {
		return ApplyPatchOutput{}, ctxerrors.Wrap(
			ErrLimitExceeded,
			"too many patch hunks",
		)
	}

	e.mutation.Lock()
	defer e.mutation.Unlock()

	prepared, err := e.preparePatch(parsed.actions)
	if err != nil {
		return ApplyPatchOutput{}, err
	}

	return e.applyPreparedPatch(ctx, prepared)
}

func (e *Executor) preparePatch(
	actions []patchAction,
) ([]preparedPatchAction, error) {
	prepared := make([]preparedPatchAction, 0, len(actions))
	claimedPaths := make(
		map[string]struct{},
		len(actions)*maxPatchPathsPerAction,
	)
	changedBytes := 0

	for index, action := range actions {
		item, err := e.preparePatchAction(action, claimedPaths)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "prepare patch action %d", index)
		}

		changedBytes += len(item.oldContent) + len(item.newContent)
		if changedBytes > e.limits.MaxPatchChangedBytes {
			return nil, ctxerrors.Wrap(
				ErrLimitExceeded,
				"patch changed content exceeds byte bound",
			)
		}

		prepared = append(prepared, item)
	}

	return prepared, nil
}

func (e *Executor) preparePatchAction(
	action patchAction,
	claimedPaths map[string]struct{},
) (preparedPatchAction, error) {
	path, err := e.resolvePath(action.path)
	if err != nil {
		return preparedPatchAction{}, ctxerrors.Wrap(err, "resolve patch path")
	}

	if err := claimPatchPath(claimedPaths, path); err != nil {
		return preparedPatchAction{}, err
	}

	item := preparedPatchAction{operation: action.operation, path: path}
	if action.destination != "" {
		item.destination, err = e.resolvePath(action.destination)
		if err != nil {
			return preparedPatchAction{}, ctxerrors.Wrap(
				err,
				"resolve patch destination",
			)
		}

		if err := claimPatchPath(claimedPaths, item.destination); err != nil {
			return preparedPatchAction{}, err
		}
	}

	switch action.operation {
	case PatchOperationAdd:
		return e.preparePatchAdd(item, action.addContent)
	case PatchOperationUpdate, PatchOperationMove:
		return e.preparePatchUpdate(item, action.hunks)
	case PatchOperationDelete:
		return e.preparePatchDelete(item)
	default:
		return preparedPatchAction{}, ctxerrors.Wrap(
			ErrInvalidPatch,
			"unknown patch operation",
		)
	}
}

func (e *Executor) preparePatchAdd(
	item preparedPatchAction,
	content []byte,
) (preparedPatchAction, error) {
	if len(content) > e.limits.MaxWriteBytes {
		return preparedPatchAction{}, ctxerrors.Wrap(
			ErrLimitExceeded,
			"patch add exceeds write byte bound",
		)
	}

	if _, err := os.Lstat(item.path); err == nil {
		return preparedPatchAction{}, ctxerrors.Wrap(
			commerr.ErrAlreadyExists,
			"patch add destination",
		)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return preparedPatchAction{}, wrapPathError(
			err,
			"stat patch add destination",
		)
	}

	item.newContent = content
	item.mode = newFileMode
	item.diff = completeFileDiff(item.path, item.path, nil, content)

	return item, nil
}

func (e *Executor) preparePatchUpdate(
	item preparedPatchAction,
	hunks []patchHunk,
) (preparedPatchAction, error) {
	if err := e.validatePatchUpdatePaths(item); err != nil {
		return preparedPatchAction{}, err
	}

	content, mode, err := readRegularFile(
		item.path,
		int64(e.limits.MaxPatchChangedBytes),
	)
	if err != nil {
		return preparedPatchAction{}, ctxerrors.Wrap(err, "read patch source")
	}

	if isBinary(content) {
		return preparedPatchAction{}, ctxerrors.Wrap(
			ErrBinaryContent,
			item.path,
		)
	}

	if err := e.requireObservedContent(item.path, content); err != nil {
		return preparedPatchAction{}, err
	}

	rewritten, err := applyPatchHunks(content, hunks)
	if err != nil {
		return preparedPatchAction{}, err
	}

	if len(rewritten) > e.limits.MaxWriteBytes {
		return preparedPatchAction{}, ctxerrors.Wrap(
			ErrLimitExceeded,
			"patched file exceeds write byte bound",
		)
	}

	item.oldContent = content
	item.newContent = rewritten
	item.mode = mode

	newPath := item.path
	if item.destination != "" {
		newPath = item.destination
	}

	item.diff = completeFileDiff(
		item.path,
		newPath,
		content,
		rewritten,
	)

	return item, nil
}

func (e *Executor) validatePatchUpdatePaths(item preparedPatchAction) error {
	if item.operation != PatchOperationMove {
		return nil
	}

	if isProtectedPatchRoot(item.path, e.workspace) {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"cannot move a protected root",
		)
	}

	return checkDestinationFree(item.destination)
}

func (e *Executor) preparePatchDelete(
	item preparedPatchAction,
) (preparedPatchAction, error) {
	if isProtectedPatchRoot(item.path, e.workspace) {
		return preparedPatchAction{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"cannot delete a protected root",
		)
	}

	content, mode, err := readRegularFile(
		item.path,
		int64(e.limits.MaxPatchChangedBytes),
	)
	if err != nil {
		return preparedPatchAction{}, ctxerrors.Wrap(
			err,
			"read patch delete source",
		)
	}

	if err := e.requireObservedContent(item.path, content); err != nil {
		return preparedPatchAction{}, err
	}

	item.oldContent = content
	item.mode = mode
	item.diff = completeFileDiff(item.path, item.path, content, nil)

	return item, nil
}

func isProtectedPatchRoot(path, workspace string) bool {
	filesystemRoot := filepath.VolumeName(path) + string(os.PathSeparator)

	return path == workspace || path == filepath.Clean(filesystemRoot)
}

func claimPatchPath(claimed map[string]struct{}, path string) error {
	path = filepath.Clean(path)
	if _, ok := claimed[path]; ok {
		return ctxerrors.Wrap(ErrPatchConflict, path)
	}

	claimed[path] = struct{}{}

	return nil
}

func completeFileDiff(
	oldPath, newPath string,
	oldContent, newContent []byte,
) string {
	diff, _ := unifiedDiff(
		oldPath,
		string(oldContent),
		string(newContent),
		[]replacement{{oldEnd: len(oldContent), newEnd: len(newContent)}},
		0,
	)
	if newPath == oldPath {
		return diff
	}

	return strings.Replace(diff, "+++ "+oldPath+"\n", "+++ "+newPath+"\n", 1)
}

func (e *Executor) applyPreparedPatch(
	ctx context.Context,
	prepared []preparedPatchAction,
) (ApplyPatchOutput, error) {
	output := ApplyPatchOutput{
		Files: make([]ApplyPatchFileResult, 0, len(prepared)),
	}
	diffs := make([]string, 0, len(prepared))

	for index, item := range prepared {
		if err := ctx.Err(); err != nil {
			output.Diff, output.DiffTruncated = boundDiff(
				strings.Join(diffs, ""),
				e.limits.MaxDiffBytes,
			)

			return output, ctxerrors.Wrap(err, "apply patch canceled")
		}

		result, err := e.applyPreparedAction(item)
		if result.Path != "" {
			output.Files = append(output.Files, result)
			diffs = append(diffs, item.diff)
		}

		if err != nil {
			output.Diff, output.DiffTruncated = boundDiff(
				strings.Join(diffs, ""),
				e.limits.MaxDiffBytes,
			)

			return output, ctxerrors.Wrapf(err, "apply patch action %d", index)
		}
	}

	output.Diff, output.DiffTruncated = boundDiff(
		strings.Join(diffs, ""),
		e.limits.MaxDiffBytes,
	)

	return output, nil
}

func (e *Executor) applyPreparedAction(
	item preparedPatchAction,
) (ApplyPatchFileResult, error) {
	switch item.operation {
	case PatchOperationAdd:
		return e.applyPatchWrite(item, true)
	case PatchOperationUpdate:
		return e.applyPatchWrite(item, false)
	case PatchOperationDelete:
		return e.applyPatchDelete(item)
	case PatchOperationMove:
		return e.applyPatchMove(item)
	default:
		return ApplyPatchFileResult{}, ctxerrors.Wrap(
			ErrInvalidPatch,
			"unknown prepared patch operation",
		)
	}
}

func (e *Executor) applyPatchWrite(
	item preparedPatchAction,
	createParents bool,
) (ApplyPatchFileResult, error) {
	if createParents {
		parent := filepath.Dir(item.path)
		if err := os.MkdirAll(parent, newDirectoryMode); err != nil {
			return ApplyPatchFileResult{}, wrapPathError(
				err,
				"create patch destination parent",
			)
		}
	}

	write := writeFileAtomic
	if createParents {
		write = writeNewFileAtomic
	}

	if err := write(item.path, item.newContent, item.mode); err != nil {
		return ApplyPatchFileResult{}, ctxerrors.Wrap(err, "write patched file")
	}

	hash := hashBytes(item.newContent)
	e.observeContent(item.path, hash)

	return ApplyPatchFileResult{
		Operation: item.operation,
		Outcome:   PatchOutcomeApplied,
		Path:      item.path,
		Bytes:     len(item.newContent),
		SHA256:    hash,
	}, nil
}

func (e *Executor) applyPatchDelete(
	item preparedPatchAction,
) (ApplyPatchFileResult, error) {
	if err := os.Remove(item.path); err != nil {
		return ApplyPatchFileResult{}, wrapPathError(err, "delete patched file")
	}

	return ApplyPatchFileResult{
		Operation: PatchOperationDelete,
		Outcome:   PatchOutcomeApplied,
		Path:      item.path,
	}, nil
}

func (e *Executor) applyPatchMove(
	item preparedPatchAction,
) (ApplyPatchFileResult, error) {
	parent := filepath.Dir(item.destination)
	if err := os.MkdirAll(parent, newDirectoryMode); err != nil {
		return ApplyPatchFileResult{}, wrapPathError(
			err,
			"create patch move destination parent",
		)
	}

	if err := writeNewFileAtomic(
		item.destination,
		item.newContent,
		item.mode,
	); err != nil {
		return ApplyPatchFileResult{}, ctxerrors.Wrap(
			err,
			"write patch move destination",
		)
	}

	hash := hashBytes(item.newContent)
	e.observeContent(item.destination, hash)
	partial := ApplyPatchFileResult{
		Operation:   PatchOperationMove,
		Outcome:     PatchOutcomePartial,
		Path:        item.path,
		Destination: item.destination,
		Bytes:       len(item.newContent),
		SHA256:      hash,
	}

	if err := os.Remove(item.path); err != nil {
		return partial, wrapPathError(err, "remove patch move source")
	}

	return e.finishPatchMove(item), nil
}

func (e *Executor) finishPatchMove(
	item preparedPatchAction,
) ApplyPatchFileResult {
	hash := hashBytes(item.newContent)
	e.observeContent(item.destination, hash)

	return ApplyPatchFileResult{
		Operation:   PatchOperationMove,
		Outcome:     PatchOutcomeApplied,
		Path:        item.path,
		Destination: item.destination,
		Bytes:       len(item.newContent),
		SHA256:      hash,
	}
}
