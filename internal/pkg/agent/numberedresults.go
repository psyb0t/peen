package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/modeljson"
)

const (
	// numberedCutFooterFormat replaces readFile's own range line when the
	// result cap keeps only part of a window. It keeps the tool's wording,
	// so the model pages the same way it does for a window the tool cut.
	//
	//nolint:lll // One model-facing line.
	numberedCutFooterFormat = "[lines %d-%d of %d; next offset %d; the rest of this window did not fit the tool result size limit]"

	numberedFieldLastLine   = "lastLine"
	numberedFieldNextOffset = "nextOffset"
	numberedFieldTruncated  = "truncated"
	numberedLinePrefixEnd   = "\t"
)

// Decode the content before cutting, so escaped newlines in a file are
// never mistaken for numbered line boundaries.
func numberedResult(content string) (map[string]json.RawMessage, []string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &fields) != nil ||
		fields["firstLine"] == nil || fields["totalLines"] == nil {
		return nil, nil
	}

	var text string
	if json.Unmarshal(fields["content"], &text) != nil ||
		!strings.Contains(text, "\t") {
		return nil, nil
	}

	return fields, strings.Split(text, "\n")
}

func encodeNumberedResult(
	fields map[string]json.RawMessage,
	lines []string,
) (string, error) {
	text, err := modeljson.Marshal(strings.Join(lines, "\n"))
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode numbered lines")
	}

	fields["content"] = text

	encoded, err := modeljson.Marshal(fields)
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode numbered result")
	}

	return string(encoded), nil
}

// numberedLineNumber returns the file line number a numbered content line
// starts with, or false for a line without one, such as a range note.
func numberedLineNumber(line string) (int, bool) {
	prefix, _, found := strings.Cut(line, numberedLinePrefixEnd)
	if !found {
		return 0, false
	}

	number, err := strconv.Atoi(strings.TrimSpace(prefix))
	if err != nil {
		return 0, false
	}

	return number, true
}

// cutNumberedResult keeps the first lines of a readFile window and rewrites
// the window's metadata to describe exactly those lines, so lastLine,
// nextOffset and truncated tell the model where to continue.
func cutNumberedResult(
	fields map[string]json.RawMessage,
	kept []string,
) (string, error) {
	var firstLine, totalLines int
	if err := json.Unmarshal(fields["firstLine"], &firstLine); err != nil {
		return "", ctxerrors.Wrap(err, "decode numbered first line")
	}

	if err := json.Unmarshal(fields["totalLines"], &totalLines); err != nil {
		return "", ctxerrors.Wrap(err, "decode numbered total lines")
	}

	lastLine := firstLine - 1

	for _, line := range kept {
		if number, ok := numberedLineNumber(line); ok {
			lastLine = number
		}
	}

	nextOffset := lastLine + 1
	cut := maps.Clone(fields)
	cut[numberedFieldLastLine] = json.RawMessage(strconv.Itoa(lastLine))
	cut[numberedFieldNextOffset] = json.RawMessage(strconv.Itoa(nextOffset))
	cut[numberedFieldTruncated] = json.RawMessage("true")

	footer := fmt.Sprintf(numberedCutFooterFormat,
		firstLine, lastLine, totalLines, nextOffset)

	return encodeNumberedResult(cut, append(kept, footer))
}

func boundNumberedResult(
	ctx context.Context,
	content string,
	options workflowToolOptions,
) (string, bool, error) {
	fields, lines := numberedResult(content)
	if fields == nil {
		return "", false, nil
	}

	fits := workflowToolFits(options)
	if ok, err := fits(content); ok || err != nil {
		return content, true, err
	}

	best, err := fitNumberedLines(ctx, fields, lines, fits)
	if err != nil {
		return "", true, err
	}

	if best == "" {
		bounded, err := boundWorkflowToolContent(ctx,
			workflowToolTruncatedMarker, options)

		return bounded, true, err
	}

	return best, true, nil
}

func fitNumberedLines(
	ctx context.Context,
	fields map[string]json.RawMessage,
	lines []string,
	fits func(string) (bool, error),
) (string, error) {
	best := ""

	low, high := 0, len(lines)-1
	for low <= high {
		if err := ctx.Err(); err != nil {
			return "", ctxerrors.Wrap(err, "fit numbered result")
		}

		middle := low + (high-low)/workflowBinarySplitFactor
		kept := append([]string(nil), lines[:middle]...)

		candidate, err := cutNumberedResult(fields, kept)
		if err != nil {
			return "", err
		}

		ok, err := fits(candidate)
		if err != nil {
			return "", err
		}

		if !ok {
			high = middle - 1

			continue
		}

		best, low = candidate, middle+1
	}

	return best, nil
}

func clipNumberedResult(text string, limit int) (string, bool) {
	opening, inner, closing, wrapped := splitToolOutput(text)
	if !wrapped {
		inner = text
	}

	fields, lines := numberedResult(inner)
	if fields == nil {
		return "", false
	}

	room := max(limit-len(opening)-len(closing), 0) /
		compactionToolResultEnds
	head, tail := []string{}, []string{}

	used := 0
	for _, line := range lines {
		used += len(line) + 1
		if used > room {
			break
		}

		head = append(head, line)
	}

	used = 0
	for index := len(lines) - 1; index >= len(head); index-- {
		used += len(lines[index]) + 1
		if used > room {
			break
		}

		tail = append([]string{lines[index]}, tail...)
	}

	head = append(head, workflowToolTruncatedMarker)

	encoded, err := encodeNumberedResult(fields, append(head, tail...))
	if err != nil {
		return compactionToolResultOmitted, true
	}

	return opening + encoded + closing, true
}
