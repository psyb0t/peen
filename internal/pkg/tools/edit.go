package tools

import (
	"bytes"
	"context"
	"sort"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// editMatch records where one edit's Old text was found in the original
// content, plus the replacement text it carries.
type editMatch struct {
	start   int
	end     int
	newText []byte
}

// EditFile applies one or more exact-text replacements to a file the current
// turn has already observed. Every edit's Old text must appear in the
// original content exactly once; every match is located against that
// original content, so edit order never changes what another edit sees. New
// may be empty to delete the matched text. Edits whose matched ranges
// overlap fail the entire call, and nothing is written to disk unless every
// edit resolves to a single, non-overlapping match.
func (e *Executor) EditFile(
	ctx context.Context,
	input EditFileInput,
) (EditFileOutput, error) {
	if err := ctx.Err(); err != nil {
		return EditFileOutput{}, ctxerrors.Wrap(
			err,
			"check context before edit",
		)
	}

	path, err := e.resolveEditPath(input.Path)
	if err != nil {
		return EditFileOutput{}, err
	}

	if err := validateEdits(input.Edits, e.Limits().MaxEdits); err != nil {
		return EditFileOutput{}, err
	}

	e.mutation.Lock()
	defer e.mutation.Unlock()

	return e.applyEditsLocked(path, input.Edits)
}

// applyEditsLocked runs the read-check-write sequence for one edit call. The
// caller already holds the mutation lock.
func (e *Executor) applyEditsLocked(
	path string,
	edits []TextEdit,
) (EditFileOutput, error) {
	content, mode, err := readRegularFile(path, int64(e.Limits().MaxWriteBytes))
	if err != nil {
		return EditFileOutput{}, ctxerrors.Wrap(err, "read file to edit")
	}

	if isBinary(content) {
		return EditFileOutput{}, ctxerrors.Wrap(ErrBinaryContent, path)
	}

	if err := e.requireObservedContent(path, content); err != nil {
		return EditFileOutput{}, err
	}

	matches, err := matchEdits(content, edits)
	if err != nil {
		return EditFileOutput{}, err
	}

	if err := checkOverlaps(matches); err != nil {
		return EditFileOutput{}, err
	}

	rewritten, replacements := applyMatches(content, matches)

	diff, truncated := unifiedDiff(
		path,
		string(content),
		string(rewritten),
		replacements,
		e.Limits().MaxDiffBytes,
	)

	if err := writeFileAtomic(path, rewritten, mode); err != nil {
		return EditFileOutput{}, ctxerrors.Wrap(err, "write edited file")
	}

	newHash := hashBytes(rewritten)
	e.observeContent(path, newHash)

	return EditFileOutput{
		Path:          path,
		Diff:          diff,
		DiffTruncated: truncated,
		Applied:       len(edits),
		SHA256:        newHash,
	}, nil
}

// resolveEditPath resolves the tool path, rejecting an empty one. edit_file
// never creates a file, so unlike other tools it has no "workspace itself"
// default to fall back on.
func (e *Executor) resolveEditPath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"path is required",
		)
	}

	path, err := e.resolvePath(raw)
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve edit path")
	}

	return path, nil
}

// validateEdits checks the edit list shape before any file is touched.
func validateEdits(edits []TextEdit, maxEdits int) error {
	if len(edits) == 0 {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"at least one edit is required",
		)
	}

	if len(edits) > maxEdits {
		return ctxerrors.Wrap(ErrLimitExceeded, "too many edits")
	}

	for i, edit := range edits {
		if edit.Old == "" {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"edit %d: old text is required",
				i,
			)
		}
	}

	return nil
}

// matchEdits locates each edit's Old text in the original content. Every
// edit is matched against the same, unmodified content, so earlier edits in
// the list never affect what a later edit finds.
func matchEdits(content []byte, edits []TextEdit) ([]editMatch, error) {
	matches := make([]editMatch, len(edits))

	for i, edit := range edits {
		old := []byte(edit.Old)

		switch count := bytes.Count(content, old); {
		case count == 0:
			return nil, ctxerrors.Wrapf(ErrMatchNotFound, "edit %d", i)
		case count > 1:
			return nil, ctxerrors.Wrapf(ErrMatchNotUnique, "edit %d", i)
		}

		start := bytes.Index(content, old)

		matches[i] = editMatch{
			start:   start,
			end:     start + len(old),
			newText: []byte(edit.New),
		}
	}

	return matches, nil
}

// checkOverlaps rejects two matched ranges that share any byte. Ranges that
// merely touch end-to-start are not overlapping.
func checkOverlaps(matches []editMatch) error {
	ordered := sortedByStart(matches)

	for i := 1; i < len(ordered); i++ {
		if ordered[i].start < ordered[i-1].end {
			return ctxerrors.Wrap(ErrOverlappingEdits, "edit ranges overlap")
		}
	}

	return nil
}

// applyMatches rewrites the content by substituting every match in
// ascending order of its original start offset, recording each substitution
// as a diff replacement in both the original and rewritten byte space.
func applyMatches(
	content []byte,
	matches []editMatch,
) ([]byte, []replacement) {
	ordered := sortedByStart(matches)

	rewritten := make([]byte, 0, len(content))
	replacements := make([]replacement, 0, len(ordered))
	cursor := 0

	for _, m := range ordered {
		rewritten = append(rewritten, content[cursor:m.start]...)

		newStart := len(rewritten)
		rewritten = append(rewritten, m.newText...)
		newEnd := len(rewritten)

		replacements = append(replacements, replacement{
			oldStart: m.start,
			oldEnd:   m.end,
			newStart: newStart,
			newEnd:   newEnd,
		})

		cursor = m.end
	}

	rewritten = append(rewritten, content[cursor:]...)

	return rewritten, replacements
}

// sortedByStart returns a copy of matches ordered by their original start
// offset, leaving the input slice untouched.
func sortedByStart(matches []editMatch) []editMatch {
	ordered := make([]editMatch, len(matches))
	copy(ordered, matches)

	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].start < ordered[j].start
	})

	return ordered
}
