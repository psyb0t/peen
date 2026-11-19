package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/modeljson"
)

const (
	// objectCutHeadFormat and objectCutTailFormat mark the part of a string
	// field the result cap left out, at the end or at the start.
	objectCutHeadFormat = "\n[%d more bytes left out to fit the tool " +
		"result size limit]"
	objectCutTailFormat = "[%d earlier bytes left out to fit the tool " +
		"result size limit]\n"

	objectFieldTruncated = "truncated"
	objectFieldResultCut = "resultCut"
	objectFieldStdout    = "stdout"
	objectFieldStderr    = "stderr"
)

// tailPreservingResultFields hold output whose newest lines matter most: a
// command's last lines carry its summary and its final error.
//
//nolint:gochecknoglobals // Read-only lookup table.
var tailPreservingResultFields = map[string]bool{
	objectFieldStdout: true,
	objectFieldStderr: true,
}

// objectFieldKind is how one field of a result object can be shortened.
type objectFieldKind int

const (
	objectFieldString objectFieldKind = 1
	objectFieldArray  objectFieldKind = 2
)

// objectCutField is one field that can give up room, with its decoded value.
type objectCutField struct {
	name  string
	kind  objectFieldKind
	text  string
	items []json.RawMessage
}

// boundObjectResult shortens a JSON object result inside its fields: the
// longest strings and arrays give up room first, strings keep their head or,
// for command output, their tail, arrays keep their first items. The object
// stays valid JSON and keeps every scalar field, so state, exit codes and
// cursors survive. It reports false when content is not a JSON object.
func boundObjectResult(
	ctx context.Context,
	content string,
	fits func(string) (bool, error),
) (string, bool, error) {
	fields, isObject := decodeResultObject(content)
	if !isObject {
		return "", false, nil
	}

	candidates := objectCutCandidates(fields)
	if len(candidates) == 0 {
		return "", false, nil
	}

	cut := maps.Clone(fields)
	markObjectCut(cut)

	for _, field := range candidates {
		encoded, done, err := shrinkObjectField(ctx, cut, field, fits)
		if err != nil {
			return "", true, err
		}

		if done {
			return encoded, true, nil
		}
	}

	return "", false, nil
}

// decodeResultObject reads content as a non-empty JSON object. Anything else
// is not an object result, which is an answer, not a failure.
func decodeResultObject(content string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage

	err := json.Unmarshal([]byte(content), &fields)

	return fields, err == nil && len(fields) > 0
}

// objectCutCandidates lists the string and array fields, longest first.
func objectCutCandidates(
	fields map[string]json.RawMessage,
) []objectCutField {
	candidates := make([]objectCutField, 0, len(fields))

	for name, raw := range fields {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			candidates = append(candidates, objectCutField{
				name: name, kind: objectFieldString, text: text,
			})

			continue
		}

		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil && len(items) > 0 {
			candidates = append(candidates, objectCutField{
				name: name, kind: objectFieldArray, items: items,
			})
		}
	}

	slices.SortFunc(candidates, func(left, right objectCutField) int {
		return len(fields[right.name]) - len(fields[left.name])
	})

	return candidates
}

// markObjectCut records in the object that the result cap shortened it,
// through its own truncated field when it has one.
func markObjectCut(fields map[string]json.RawMessage) {
	if _, found := fields[objectFieldTruncated]; found {
		fields[objectFieldTruncated] = json.RawMessage("true")

		return
	}

	fields[objectFieldResultCut] = json.RawMessage("true")
}

// shrinkObjectField finds the most of one field that still lets the object
// fit. It leaves the field emptied in fields and reports false when even an
// empty field does not fit, so the next candidate is tried.
func shrinkObjectField(
	ctx context.Context,
	fields map[string]json.RawMessage,
	field objectCutField,
	fits func(string) (bool, error),
) (string, bool, error) {
	size := len(field.text)
	if field.kind == objectFieldArray {
		size = len(field.items)
	}

	best := ""

	low, high := 0, size-1
	for low <= high {
		if err := ctx.Err(); err != nil {
			return "", false, ctxerrors.Wrap(err, "fit tool result object")
		}

		middle := low + (high-low)/workflowBinarySplitFactor

		encoded, err := encodeObjectWithField(fields, field, middle)
		if err != nil {
			return "", false, err
		}

		ok, err := fits(encoded)
		if err != nil {
			return "", false, err
		}

		if !ok {
			high = middle - 1

			continue
		}

		best, low = encoded, middle+1
	}

	if best != "" {
		return best, true, nil
	}

	if _, err := encodeObjectWithField(fields, field, 0); err != nil {
		return "", false, err
	}

	return "", false, nil
}

// encodeObjectWithField sets field to its first keep bytes or items, or its
// last keep bytes for command output, and encodes the object.
func encodeObjectWithField(
	fields map[string]json.RawMessage,
	field objectCutField,
	keep int,
) (string, error) {
	var value any = field.items[:min(keep, len(field.items))]
	if field.kind == objectFieldString {
		value = cutObjectString(field, keep)
	}

	raw, err := modeljson.Marshal(value)
	if err != nil {
		return "", ctxerrors.Wrapf(err, "encode cut field %s", field.name)
	}

	fields[field.name] = raw

	encoded, err := modeljson.Marshal(fields)
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode cut tool result")
	}

	return string(encoded), nil
}

// cutObjectString keeps keep bytes of a string field on a rune boundary and
// says how much it left out.
func cutObjectString(field objectCutField, keep int) string {
	text := field.text
	if keep >= len(text) {
		return text
	}

	if !tailPreservingResultFields[field.name] {
		head := workflowUTF8Prefix(text, keep)

		return head + fmt.Sprintf(objectCutHeadFormat, len(text)-len(head))
	}

	start := len(text) - keep
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}

	tail := text[start:]
	if newline := strings.IndexByte(tail, '\n'); newline >= 0 &&
		newline < len(tail)-1 {
		tail = tail[newline+1:]
	}

	return fmt.Sprintf(objectCutTailFormat, len(text)-len(tail)) + tail
}
