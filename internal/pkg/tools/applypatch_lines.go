package tools

import (
	"bytes"

	"github.com/psyb0t/ctxerrors"
)

const (
	patchLineContext         = ' '
	patchLineAdd             = '+'
	patchLineDelete          = '-'
	supportedLineEndingKinds = 2
)

type patchSourceLine struct {
	text   string
	ending string
}

func applyPatchHunks(content []byte, hunks []patchHunk) ([]byte, error) {
	lines := splitPatchSourceLines(content)
	lineEnding := preferredLineEnding(lines)
	cursor := 0

	for index, hunk := range hunks {
		anchorCursor, err := locatePatchAnchor(lines, cursor, hunk.anchor)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "hunk %d anchor", index)
		}

		matchIndex, oldCount, err := locatePatchHunk(lines, anchorCursor, hunk)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "hunk %d", index)
		}

		replacement := buildPatchReplacement(
			lines[matchIndex:matchIndex+oldCount],
			hunk.lines,
			lineEnding,
		)
		if matchIndex+oldCount == len(lines) && len(lines) > 0 &&
			lines[len(lines)-1].ending == "" && len(replacement) > 0 {
			for lineIndex := range replacement[:len(replacement)-1] {
				if replacement[lineIndex].ending == "" {
					replacement[lineIndex].ending = lineEnding
				}
			}

			replacement[len(replacement)-1].ending = ""
		}

		lines = replacePatchLines(lines, matchIndex, oldCount, replacement)
		cursor = matchIndex + len(replacement)
	}

	return joinPatchSourceLines(lines), nil
}

func splitPatchSourceLines(content []byte) []patchSourceLine {
	lines := make([]patchSourceLine, 0, bytes.Count(content, []byte{'\n'})+1)
	for len(content) > 0 {
		newline := bytes.IndexByte(content, '\n')
		if newline < 0 {
			lines = append(lines, patchSourceLine{text: string(content)})

			break
		}

		textEnd := newline
		ending := "\n"

		if newline > 0 && content[newline-1] == '\r' {
			textEnd--
			ending = "\r\n"
		}

		lines = append(lines, patchSourceLine{
			text:   string(content[:textEnd]),
			ending: ending,
		})
		content = content[newline+1:]
	}

	return lines
}

func preferredLineEnding(lines []patchSourceLine) string {
	lineEndings := make(map[string]int, supportedLineEndingKinds)
	preferred := "\n"
	preferredCount := 0

	for _, line := range lines {
		if line.ending == "" {
			continue
		}

		lineEndings[line.ending]++
		if lineEndings[line.ending] > preferredCount {
			preferred = line.ending
			preferredCount = lineEndings[line.ending]
		}
	}

	return preferred
}

func locatePatchAnchor(
	lines []patchSourceLine,
	cursor int,
	anchor string,
) (int, error) {
	if anchor == "" {
		return cursor, nil
	}

	match := -1

	for index := cursor; index < len(lines); index++ {
		if lines[index].text != anchor {
			continue
		}

		if match >= 0 {
			return 0, ctxerrors.Wrap(ErrPatchMatchNotUnique, "anchor")
		}

		match = index
	}

	if match < 0 {
		return 0, ctxerrors.Wrap(ErrPatchMatchNotFound, "anchor")
	}

	return match + 1, nil
}

func locatePatchHunk(
	lines []patchSourceLine,
	cursor int,
	hunk patchHunk,
) (int, int, error) {
	oldLines := patchOldLines(hunk.lines)
	if len(oldLines) == 0 {
		if hunk.endOfFile {
			return len(lines), 0, nil
		}

		return cursor, 0, nil
	}

	match := -1

	for index := cursor; index+len(oldLines) <= len(lines); index++ {
		if !patchLinesEqual(lines[index:index+len(oldLines)], oldLines) {
			continue
		}

		if hunk.endOfFile && index+len(oldLines) != len(lines) {
			continue
		}

		if match >= 0 {
			return 0, 0, ctxerrors.Wrap(ErrPatchMatchNotUnique, "hunk context")
		}

		match = index
	}

	if match < 0 {
		return 0, 0, ctxerrors.Wrap(ErrPatchMatchNotFound, "hunk context")
	}

	return match, len(oldLines), nil
}

func patchOldLines(lines []patchLine) []string {
	oldLines := make([]string, 0, len(lines))
	for _, line := range lines {
		if line.kind != patchLineAdd {
			oldLines = append(oldLines, line.text)
		}
	}

	return oldLines
}

func patchLinesEqual(source []patchSourceLine, expected []string) bool {
	for index := range expected {
		if source[index].text != expected[index] {
			return false
		}
	}

	return true
}

func buildPatchReplacement(
	source []patchSourceLine,
	lines []patchLine,
	lineEnding string,
) []patchSourceLine {
	replacement := make([]patchSourceLine, 0, len(lines))
	sourceIndex := 0

	for _, line := range lines {
		switch line.kind {
		case patchLineContext:
			replacement = append(replacement, source[sourceIndex])
			sourceIndex++
		case patchLineDelete:
			sourceIndex++
		case patchLineAdd:
			replacement = append(replacement, patchSourceLine{
				text:   line.text,
				ending: lineEnding,
			})
		}
	}

	return replacement
}

func replacePatchLines(
	lines []patchSourceLine,
	index, oldCount int,
	replacement []patchSourceLine,
) []patchSourceLine {
	capacity := len(lines) - oldCount + len(replacement)
	rewritten := make([]patchSourceLine, 0, capacity)
	rewritten = append(rewritten, lines[:index]...)
	rewritten = append(rewritten, replacement...)
	rewritten = append(rewritten, lines[index+oldCount:]...)

	return rewritten
}

func joinPatchSourceLines(lines []patchSourceLine) []byte {
	var content bytes.Buffer
	for _, line := range lines {
		content.WriteString(line.text)
		content.WriteString(line.ending)
	}

	return content.Bytes()
}
