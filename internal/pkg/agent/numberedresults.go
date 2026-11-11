package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
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
	text, err := json.Marshal(strings.Join(lines, "\n"))
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode numbered lines")
	}

	fields["content"] = text

	encoded, err := json.Marshal(fields)
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode numbered result")
	}

	return string(encoded), nil
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

	counter := options.Counter
	if counter == nil {
		counter = elelem.DefaultTokenCounter()
	}

	fits := func(text string) (bool, error) {
		if limit := workflowToolByteLimit(options); limit > 0 &&
			len(text) > limit {
			return false, nil
		}

		if options.MaxResultTokens <= 0 {
			return true, nil
		}

		count, err := workflowContentTokens(counter, text)

		return count <= options.MaxResultTokens, err
	}
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

		candidate, err := encodeNumberedResult(fields,
			append(kept, workflowToolTruncatedMarker))
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
