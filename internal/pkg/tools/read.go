package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const (
	// firstLine is the 1-based index of the first line of a file. An Offset
	// of zero or a negative value collapses onto this default.
	firstLine = 1

	lineSeparator = '\n'
	// readBufferBytes is the read buffer of a streamed readFile. A longer
	// line is read in several pieces.
	readBufferBytes = 64 * 1024
	// readCancelCheckLines is how many lines a streamed read scans between
	// checks of its context.
	readCancelCheckLines = 4096
)

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

	if mediaType := imageMediaTypeForPath(resolved); mediaType != "" {
		return e.readImageFile(resolved, mediaType)
	}

	if isPDFPath(resolved) {
		return e.readPDFFile(ctx, resolved, input)
	}

	file, err := e.openRegularFile(resolved)
	if err != nil {
		return ReadFileOutput{}, ctxerrors.Wrap(err, "read file")
	}

	output, readErr := e.readLineWindow(ctx, file, resolved, input)
	if err := errors.Join(readErr, file.Close()); err != nil {
		return ReadFileOutput{}, ctxerrors.Wrap(err, "read file")
	}

	e.observeContent(resolved, output.SHA256)

	return output, nil
}

// openRegularFile opens name for a streamed read and reports its size.
func (e *Executor) openRegularFile(name string) (*sizedFile, error) {
	file, err := e.filesystem.Open(name)
	if err != nil {
		return nil, wrapPathError(err, "open filesystem file")
	}

	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(ctxerrors.Wrap(err, "stat read file"),
			file.Close())
	}

	if !info.Mode().IsRegular() {
		return nil, errors.Join(ctxerrors.Wrap(ErrNotRegularFile,
			"bounded file read"), file.Close())
	}

	return &sizedFile{ReadCloser: file, size: info.Size()}, nil
}

// readLineWindow streams the file once. It hashes and counts every line, and
// keeps only the requested window, which ends early when the next line would
// take it past MaxReadBytes. A file of any size can be read this way, a
// window at a time. A line longer than MaxReadBytes on its own is refused.
// An offset past the end of the file returns an empty window.
func (e *Executor) readLineWindow(
	ctx context.Context,
	file *sizedFile,
	resolved string,
	input ReadFileInput,
) (ReadFileOutput, error) {
	hasher := sha256.New()
	reader := bufio.NewReaderSize(io.TeeReader(file, hasher), readBufferBytes)

	prefix, err := reader.Peek(binarySniffBytes)
	if err != nil && !errors.Is(err, io.EOF) &&
		!errors.Is(err, bufio.ErrBufferFull) {
		return ReadFileOutput{}, ctxerrors.Wrap(err, "sniff read file")
	}

	if isBinaryPrefix(prefix, file.size) {
		return ReadFileOutput{}, ctxerrors.Wrap(ErrBinaryContent, resolved)
	}

	window := e.newLineWindow(input)
	if err := window.scan(ctx, reader); err != nil {
		return ReadFileOutput{}, err
	}

	return window.output(resolved, hex.EncodeToString(hasher.Sum(nil))), nil
}

// newLineWindow applies the offset and limit defaults and the byte bound.
func (e *Executor) newLineWindow(input ReadFileInput) *lineWindow {
	offset := normalizeOffset(input.Offset)

	return &lineWindow{
		offset:   offset,
		limit:    e.resolveReadLimit(input.Limit),
		maxBytes: e.Limits().MaxReadBytes,
		lastLine: offset - 1,
	}
}

// sizedFile is an open file and the size it had when it was opened.
type sizedFile struct {
	io.ReadCloser

	size int64
}

// lineWindow collects the lines from offset while the line limit and the byte
// bound allow, and counts every line of the file.
type lineWindow struct {
	offset, limit, maxBytes int
	content                 bytes.Buffer
	lastLine, totalLines    int
	isFull                  bool
}

func (w *lineWindow) scan(ctx context.Context, reader *bufio.Reader) error {
	for {
		if w.totalLines%readCancelCheckLines == 0 {
			if err := ctx.Err(); err != nil {
				return ctxerrors.Wrap(err, "read file canceled")
			}
		}

		line := w.totalLines + 1
		isWanted := w.wants(line)
		start := w.content.Len()

		length, hasNewline, err := readLine(reader,
			w.room(isWanted, start), &w.content)
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		w.totalLines = line

		if !isWanted {
			continue
		}

		if err := w.take(line, start, length, hasNewline); err != nil {
			return err
		}
	}
}

// wants reports whether line belongs in the window.
func (w *lineWindow) wants(line int) bool {
	return !w.isFull && line >= w.offset && line < w.offset+w.limit
}

// room is how many bytes of the next line to keep: one more than still fits,
// so a line that does not fit is seen to overflow. A line outside the window
// keeps nothing.
func (w *lineWindow) room(isWanted bool, start int) int {
	if !isWanted {
		return 0
	}

	return w.maxBytes - start + 1
}

// take keeps the line just read when it fits under maxBytes. It refuses a
// first line that cannot fit at all, and otherwise ends the window before it.
func (w *lineWindow) take(line, start, length int, hasNewline bool) error {
	size := length
	if hasNewline {
		size++
	}

	if start+size <= w.maxBytes {
		if hasNewline {
			w.content.WriteByte(lineSeparator)
		}

		w.lastLine = line

		return nil
	}

	w.content.Truncate(start)

	if line == w.offset {
		return ctxerrors.Wrapf(ErrLimitExceeded,
			"line %d is longer than the read bound of %d bytes", line,
			w.maxBytes)
	}

	w.isFull = true

	return nil
}

func (w *lineWindow) output(resolved, sha string) ReadFileOutput {
	truncated := w.lastLine < w.totalLines
	content := w.content.String()
	nextOffset := 0

	if truncated {
		content = strings.TrimSuffix(content, string(lineSeparator))
		nextOffset = w.lastLine + 1
	}

	return ReadFileOutput{
		Path:       resolved,
		Content:    content,
		FirstLine:  w.offset,
		LastLine:   w.lastLine,
		TotalLines: w.totalLines,
		NextOffset: nextOffset,
		Truncated:  truncated,
		SHA256:     sha,
	}
}

// readLine reads one line and writes up to keep bytes of it, without its
// newline, into kept. It returns the line's full length and whether a newline
// ended it, or io.EOF when the file has no more lines.
func readLine(
	reader *bufio.Reader,
	keep int,
	kept *bytes.Buffer,
) (int, bool, error) {
	length := 0

	for {
		chunk, err := reader.ReadSlice(lineSeparator)
		if errors.Is(err, bufio.ErrBufferFull) {
			length += keepBytes(chunk, keep-length, kept)

			continue
		}

		if err == nil {
			chunk = chunk[:len(chunk)-1]
			length += keepBytes(chunk, keep-length, kept)

			return length, true, nil
		}

		if !errors.Is(err, io.EOF) {
			return 0, false, ctxerrors.Wrap(err, "read file line")
		}

		length += keepBytes(chunk, keep-length, kept)
		if length == 0 {
			return 0, false, io.EOF
		}

		return length, false, nil
	}
}

// keepBytes writes as much of chunk as room allows and returns its length.
func keepBytes(chunk []byte, room int, kept *bytes.Buffer) int {
	if room > 0 {
		kept.Write(chunk[:min(len(chunk), room)])
	}

	return len(chunk)
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
