package tools

import (
	"context"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

// firstLine is the 1-based index of the first line of a file. An Offset of
// zero or a negative value collapses onto this default.
const firstLine = 1

// ReadFile reads a bounded line window from one regular file. SHA256 is
// computed over the whole file, not the returned window, so a caller can
// detect the file changing between reads regardless of which window it
// requested.
func (e *Executor) ReadFile(
	ctx context.Context,
	input ReadFileInput,
) (ReadFileOutput, error) {
	if err := ctx.Err(); err != nil {
		return ReadFileOutput{}, ctxerrors.Wrap(err, "read file canceled")
	}

	resolved, err := e.resolvePath(input.Path)
	if err != nil {
		return ReadFileOutput{}, ctxerrors.Wrap(err, "resolve read path")
	}

	content, _, err := readRegularFile(resolved, int64(e.Limits().MaxReadBytes))
	if err != nil {
		return ReadFileOutput{}, ctxerrors.Wrap(err, "read file")
	}

	if isBinary(content) {
		return ReadFileOutput{}, ctxerrors.Wrap(ErrBinaryContent, resolved)
	}

	sha := hashBytes(content)
	output := e.buildReadWindow(resolved, string(content), sha, input)

	e.observeContent(resolved, sha)

	return output, nil
}

// buildReadWindow slices the requested line window out of the file's full
// content, applying the offset and limit defaults and bounds. An offset past
// the end of the file returns an empty window instead of an error.
func (e *Executor) buildReadWindow(
	resolved, content, sha string,
	input ReadFileInput,
) ReadFileOutput {
	lines := splitLines(content)
	totalLines := len(lines)
	offset := normalizeOffset(input.Offset)

	if offset > totalLines {
		return ReadFileOutput{
			Path:       resolved,
			FirstLine:  offset,
			LastLine:   offset - 1,
			TotalLines: totalLines,
			SHA256:     sha,
		}
	}

	limit := e.resolveReadLimit(input.Limit)
	lastLine := min(offset+limit-1, totalLines)

	truncated := lastLine < totalLines
	window := strings.Join(lines[offset-1:lastLine], "\n")

	if !truncated && strings.HasSuffix(content, "\n") {
		window += "\n"
	}

	nextOffset := 0
	if truncated {
		nextOffset = lastLine + 1
	}

	return ReadFileOutput{
		Path:       resolved,
		Content:    window,
		FirstLine:  offset,
		LastLine:   lastLine,
		TotalLines: totalLines,
		NextOffset: nextOffset,
		Truncated:  truncated,
		SHA256:     sha,
	}
}

// normalizeOffset maps a zero or negative offset onto the first line.
func normalizeOffset(offset int) int {
	if offset <= 0 {
		return firstLine
	}

	return offset
}

// resolveReadLimit applies the read-line bound: zero or a value above the
// configured limit collapses onto that limit.
func (e *Executor) resolveReadLimit(limit int) int {
	bound := e.limits.MaxReadLines

	if limit <= 0 || limit > bound {
		return bound
	}

	return limit
}
