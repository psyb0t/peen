package tools

import (
	"strings"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
)

const (
	applyPatchBeginMarker = "*** Begin Patch"
	applyPatchEndMarker   = "*** End Patch"
	patchAddPrefix        = "*** Add File: "
	patchDeletePrefix     = "*** Delete File: "
	patchUpdatePrefix     = "*** Update File: "
	patchMovePrefix       = "*** Move to: "
	patchHunkPrefix       = "@@"
	patchEndOfFileMarker  = "*** End of File"
)

type parsedPatch struct {
	actions []patchAction
	hunks   int
}

type patchAction struct {
	operation   PatchOperation
	path        string
	destination string
	addContent  []byte
	hunks       []patchHunk
}

type patchHunk struct {
	anchor    string
	lines     []patchLine
	endOfFile bool
}

type patchLine struct {
	kind byte
	text string
}

// ApplyPatchPaths returns every source and destination named by a valid patch
// in document order. It performs no filesystem operation.
func ApplyPatchPaths(document string) ([]string, error) {
	parsed, err := parseApplyPatch(document)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "parse patch paths")
	}

	paths := make([]string, 0, len(parsed.actions)*maxPatchPathsPerAction)
	for _, action := range parsed.actions {
		paths = append(paths, action.path)
		if action.destination != "" {
			paths = append(paths, action.destination)
		}
	}

	return paths, nil
}

func parseApplyPatch(document string) (parsedPatch, error) {
	if err := validatePatchDocument(document); err != nil {
		return parsedPatch{}, err
	}

	normalized := strings.ReplaceAll(document, "\r\n", "\n")

	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if len(lines) < 2 || lines[0] != applyPatchBeginMarker ||
		lines[len(lines)-1] != applyPatchEndMarker {
		return parsedPatch{}, ctxerrors.Wrap(
			ErrInvalidPatch,
			"patch must have begin and end markers",
		)
	}

	parsed := parsedPatch{}

	for index := 1; index < len(lines)-1; {
		action, next, err := parsePatchAction(lines, index)
		if err != nil {
			return parsedPatch{}, err
		}

		parsed.actions = append(parsed.actions, action)
		parsed.hunks += len(action.hunks)
		index = next
	}

	if len(parsed.actions) == 0 {
		return parsedPatch{}, ctxerrors.Wrap(
			ErrInvalidPatch,
			"patch has no actions",
		)
	}

	return parsed, nil
}

func validatePatchDocument(document string) error {
	if utf8.ValidString(document) && strings.IndexByte(document, 0) < 0 {
		return nil
	}

	return ctxerrors.Wrap(
		ErrInvalidPatch,
		"patch must be UTF-8 text without NUL bytes",
	)
}

func parsePatchAction(lines []string, index int) (patchAction, int, error) {
	header := lines[index]

	switch {
	case strings.HasPrefix(header, patchAddPrefix):
		return parseAddAction(lines, index)
	case strings.HasPrefix(header, patchDeletePrefix):
		path, err := requiredPatchPath(
			strings.TrimPrefix(header, patchDeletePrefix),
		)

		return patchAction{
			operation: PatchOperationDelete,
			path:      path,
		}, index + 1, err
	case strings.HasPrefix(header, patchUpdatePrefix):
		return parseUpdateAction(lines, index)
	default:
		return patchAction{}, index, ctxerrors.Wrapf(
			ErrInvalidPatch,
			"line %d: expected file action",
			index+1,
		)
	}
}

func parseAddAction(lines []string, index int) (patchAction, int, error) {
	path, err := requiredPatchPath(
		strings.TrimPrefix(lines[index], patchAddPrefix),
	)
	if err != nil {
		return patchAction{}, index, err
	}

	index++
	content := make([]string, 0)

	for index < len(lines)-1 && !isPatchActionHeader(lines[index]) {
		if !strings.HasPrefix(lines[index], "+") {
			return patchAction{}, index, ctxerrors.Wrapf(
				ErrInvalidPatch,
				"line %d: add lines must start with +",
				index+1,
			)
		}

		content = append(content, strings.TrimPrefix(lines[index], "+"))
		index++
	}

	addContent := []byte{}
	if len(content) > 0 {
		addContent = []byte(strings.Join(content, "\n") + "\n")
	}

	return patchAction{
		operation:  PatchOperationAdd,
		path:       path,
		addContent: addContent,
	}, index, nil
}

func parseUpdateAction(lines []string, index int) (patchAction, int, error) {
	path, err := requiredPatchPath(
		strings.TrimPrefix(lines[index], patchUpdatePrefix),
	)
	if err != nil {
		return patchAction{}, index, err
	}

	action := patchAction{operation: PatchOperationUpdate, path: path}
	index++

	index, err = parsePatchMoveHeader(lines, index, &action)
	if err != nil {
		return patchAction{}, index, err
	}

	action.hunks, index, err = parseUpdateHunks(lines, index)
	if err != nil {
		return patchAction{}, index, err
	}

	if action.operation == PatchOperationUpdate && len(action.hunks) == 0 {
		return patchAction{}, index, ctxerrors.Wrap(
			ErrInvalidPatch,
			"update has no hunks",
		)
	}

	return action, index, nil
}

func parsePatchMoveHeader(
	lines []string,
	index int,
	action *patchAction,
) (int, error) {
	isMoveHeader := index < len(lines)-1 &&
		strings.HasPrefix(lines[index], patchMovePrefix)
	if !isMoveHeader {
		return index, nil
	}

	destination, err := requiredPatchPath(
		strings.TrimPrefix(lines[index], patchMovePrefix),
	)
	if err != nil {
		return index, err
	}

	action.destination = destination
	action.operation = PatchOperationMove

	return index + 1, nil
}

func parseUpdateHunks(
	lines []string,
	index int,
) ([]patchHunk, int, error) {
	hunks := make([]patchHunk, 0)

	for index < len(lines)-1 && !isPatchActionHeader(lines[index]) {
		if !strings.HasPrefix(lines[index], patchHunkPrefix) {
			return nil, index, ctxerrors.Wrapf(
				ErrInvalidPatch,
				"line %d: expected update hunk",
				index+1,
			)
		}

		hunk, next, parseErr := parsePatchHunk(lines, index)
		if parseErr != nil {
			return nil, index, parseErr
		}

		hunks = append(hunks, hunk)
		index = next
	}

	return hunks, index, nil
}

func parsePatchHunk(lines []string, index int) (patchHunk, int, error) {
	header := lines[index]
	hunk := patchHunk{anchor: strings.TrimSpace(
		strings.TrimPrefix(header, patchHunkPrefix),
	)}
	index++

	for !isPatchHunkBoundary(lines, index) {
		if lines[index] == patchEndOfFileMarker {
			hunk.endOfFile = true
			index++

			break
		}

		line, err := parsePatchLine(lines[index], index)
		if err != nil {
			return patchHunk{}, index, err
		}

		hunk.lines = append(hunk.lines, line)
		index++
	}

	if err := validatePatchHunk(hunk); err != nil {
		return patchHunk{}, index, err
	}

	return hunk, index, nil
}

func isPatchHunkBoundary(lines []string, index int) bool {
	return index >= len(lines)-1 || isPatchActionHeader(lines[index]) ||
		strings.HasPrefix(lines[index], patchHunkPrefix)
}

func parsePatchLine(value string, index int) (patchLine, error) {
	if value == "" {
		return patchLine{}, ctxerrors.Wrapf(
			ErrInvalidPatch,
			"line %d: invalid hunk line",
			index+1,
		)
	}

	switch value[0] {
	case patchLineContext, patchLineAdd, patchLineDelete:
		return patchLine{kind: value[0], text: value[1:]}, nil
	default:
		return patchLine{}, ctxerrors.Wrapf(
			ErrInvalidPatch,
			"line %d: invalid hunk line",
			index+1,
		)
	}
}

func validatePatchHunk(hunk patchHunk) error {
	if len(hunk.lines) == 0 {
		return ctxerrors.Wrap(ErrInvalidPatch, "empty update hunk")
	}

	hasChange := false

	for _, line := range hunk.lines {
		if line.kind == patchLineAdd || line.kind == patchLineDelete {
			hasChange = true

			break
		}
	}

	if !hasChange {
		return ctxerrors.Wrap(ErrInvalidPatch, "hunk has no changes")
	}

	return nil
}

func requiredPatchPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", ctxerrors.Wrap(ErrInvalidPatch, "file path is required")
	}

	return path, nil
}

func isPatchActionHeader(line string) bool {
	return strings.HasPrefix(line, patchAddPrefix) ||
		strings.HasPrefix(line, patchDeletePrefix) ||
		strings.HasPrefix(line, patchUpdatePrefix)
}
