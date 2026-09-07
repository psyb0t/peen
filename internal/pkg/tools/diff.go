package tools

import (
	"strconv"
	"strings"
)

const (
	diffContextLines = 3
	diffTruncatedTag = "... diff truncated ...\n"
)

// replacement records one applied edit as byte offsets into the original and
// the rewritten content. edit_file collects these while it substitutes text,
// so the diff is rendered from known ranges instead of being inferred.
type replacement struct {
	oldStart int
	oldEnd   int
	newStart int
	newEnd   int
}

type diffHunk struct {
	oldStart int
	oldEnd   int
	newStart int
	newEnd   int
}

// unifiedDiff renders the applied replacements as a unified diff bounded to
// maxBytes. The second result reports that the diff was cut short.
func unifiedDiff(
	path string,
	oldContent string,
	newContent string,
	replacements []replacement,
	maxBytes int,
) (string, bool) {
	if len(replacements) == 0 {
		return "", false
	}

	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	hunks := mergeHunks(
		spansToHunks(replacements, oldContent, newContent, oldLines, newLines),
		len(oldLines),
		len(newLines),
	)

	builder := &strings.Builder{}
	builder.WriteString("--- " + path + "\n")
	builder.WriteString("+++ " + path + "\n")

	for _, hunk := range hunks {
		writeHunk(builder, hunk, oldLines, newLines)
	}

	return boundDiff(builder.String(), maxBytes)
}

func spansToHunks(
	replacements []replacement,
	oldContent string,
	newContent string,
	oldLines []string,
	newLines []string,
) []diffHunk {
	hunks := make([]diffHunk, 0, len(replacements))

	for _, item := range replacements {
		oldTail := endOffset(item.oldStart, item.oldEnd)
		newTail := endOffset(item.newStart, item.newEnd)
		oldFirst := lineOfOffset(oldContent, item.oldStart)
		oldLast := lineOfOffset(oldContent, oldTail)
		newFirst := lineOfOffset(newContent, item.newStart)
		newLast := lineOfOffset(newContent, newTail)

		hunks = append(hunks, diffHunk{
			oldStart: clampLine(oldFirst-diffContextLines, len(oldLines)),
			oldEnd:   clampLine(oldLast+diffContextLines, len(oldLines)),
			newStart: clampLine(newFirst-diffContextLines, len(newLines)),
			newEnd:   clampLine(newLast+diffContextLines, len(newLines)),
		})
	}

	return hunks
}

// mergeHunks folds overlapping or touching context windows into one hunk so
// the rendered diff never prints the same line twice.
func mergeHunks(hunks []diffHunk, oldTotal, newTotal int) []diffHunk {
	if len(hunks) == 0 {
		return nil
	}

	merged := []diffHunk{hunks[0]}

	for _, hunk := range hunks[1:] {
		last := &merged[len(merged)-1]
		if hunk.oldStart > last.oldEnd+1 && hunk.newStart > last.newEnd+1 {
			merged = append(merged, hunk)

			continue
		}

		last.oldEnd = max(last.oldEnd, hunk.oldEnd)
		last.newEnd = max(last.newEnd, hunk.newEnd)
	}

	for index := range merged {
		merged[index].oldEnd = clampLine(merged[index].oldEnd, oldTotal)
		merged[index].newEnd = clampLine(merged[index].newEnd, newTotal)
	}

	return merged
}

func writeHunk(
	builder *strings.Builder,
	hunk diffHunk,
	oldLines []string,
	newLines []string,
) {
	oldCount := hunk.oldEnd - hunk.oldStart + 1
	newCount := hunk.newEnd - hunk.newStart + 1

	builder.WriteString("@@ -")
	builder.WriteString(strconv.Itoa(hunk.oldStart))
	builder.WriteString(",")
	builder.WriteString(strconv.Itoa(oldCount))
	builder.WriteString(" +")
	builder.WriteString(strconv.Itoa(hunk.newStart))
	builder.WriteString(",")
	builder.WriteString(strconv.Itoa(newCount))
	builder.WriteString(" @@\n")

	for _, line := range sliceLines(oldLines, hunk.oldStart, hunk.oldEnd) {
		builder.WriteString("-" + line + "\n")
	}

	for _, line := range sliceLines(newLines, hunk.newStart, hunk.newEnd) {
		builder.WriteString("+" + line + "\n")
	}
}

func boundDiff(diff string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(diff) <= maxBytes {
		return diff, false
	}

	cut := maxBytes
	if newline := strings.LastIndexByte(diff[:cut], '\n'); newline > 0 {
		cut = newline + 1
	}

	return diff[:cut] + diffTruncatedTag, true
}

// splitLines returns the content's lines without their terminators. A final
// newline does not produce a trailing empty line.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}

	trimmed := strings.TrimSuffix(content, "\n")

	return strings.Split(trimmed, "\n")
}

// lineOfOffset reports the 1-based line containing the byte offset.
func lineOfOffset(content string, offset int) int {
	if offset <= 0 {
		return 1
	}

	if offset > len(content) {
		offset = len(content)
	}

	return strings.Count(content[:offset], "\n") + 1
}

// endOffset points at the last byte of a range so a replacement ending on a
// newline is not attributed to the following line.
func endOffset(start, end int) int {
	if end <= start {
		return start
	}

	return end - 1
}

func clampLine(line, total int) int {
	if line < 1 {
		return 1
	}

	if total > 0 && line > total {
		return total
	}

	return line
}

func sliceLines(lines []string, first, last int) []string {
	if len(lines) == 0 || first > last {
		return nil
	}

	start := clampLine(first, len(lines)) - 1
	end := clampLine(last, len(lines))

	return lines[start:end]
}
