package tools

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

const (
	// maxReportedMatchLines bounds how many line numbers a match error lists.
	maxReportedMatchLines = 10

	firstLineHintFormat = "; its first line %q appears at lines %s, so " +
		"a later line of the old text differs from the file"
	lineNumberSeparator   = ", "
	lineNumbersMoreFormat = " and %d more"
)

// matchLineNumbers returns the 1-based line each occurrence of old starts on,
// at most maxReportedMatchLines of them.
func matchLineNumbers(content, old []byte) []int {
	if len(old) == 0 {
		return nil
	}

	lines := []int{}
	line, consumed := 1, 0

	for offset := 0; len(lines) < maxReportedMatchLines; {
		found := bytes.Index(content[offset:], old)
		if found < 0 {
			break
		}

		start := offset + found
		line += bytes.Count(content[consumed:start], []byte("\n"))
		consumed = start

		lines = append(lines, line)
		offset = start + 1
	}

	return lines
}

// joinLineNumbers lists line numbers for a message.
func joinLineNumbers(lines []int) string {
	parts := make([]string, len(lines))
	for index, line := range lines {
		parts[index] = strconv.Itoa(line)
	}

	return strings.Join(parts, lineNumberSeparator)
}

// firstLineHint says where the first line of an unmatched old text does
// appear, so the model can find the line that differs. It is empty when the
// first line does not appear either.
func firstLineHint(content, old []byte) string {
	first, _, _ := bytes.Cut(old, []byte("\n"))
	first = bytes.TrimSpace(first)

	if len(first) == 0 {
		return ""
	}

	lines := []int{}

	for index, line := range bytes.Split(content, []byte("\n")) {
		if !bytes.Contains(line, first) {
			continue
		}

		lines = append(lines, index+1)
	}

	if len(lines) == 0 {
		return ""
	}

	listed := joinLineNumbers(lines[:min(len(lines), maxReportedMatchLines)])
	if extra := len(lines) - maxReportedMatchLines; extra > 0 {
		listed += fmt.Sprintf(lineNumbersMoreFormat, extra)
	}

	return fmt.Sprintf(firstLineHintFormat, string(first), listed)
}
