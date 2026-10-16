package asyncapirt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// Matcher pins the JSON value at Pointer (RFC 6901) to Value, given as JSON
// text.
type Matcher struct {
	Pointer string
	Value   string
}

// Candidate is one message a client can receive.
type Candidate struct {
	Name     string
	Matchers []Matcher
	Decode   func(data []byte) (any, error)
	// Fallback marks a candidate whose payload has no schema to check, so it
	// accepts any JSON. It is only tried when no other candidate matches.
	Fallback bool
}

// DecodeMessage works out which candidate data is and decodes it.
//
// Candidates whose matchers all hold are tried first. When none match that
// way, every candidate without matchers is decoded strictly, and exactly one
// must succeed. When none succeeds, fallback candidates are tried the same
// way. A candidate whose matchers fail is never tried.
func DecodeMessage(data []byte, candidates []Candidate) (string, any, error) {
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return "", nil, ctxerrors.Wrapf(
			commerr.ErrCouldNotDecode,
			"message is not JSON: %v",
			err,
		)
	}

	var pinned, unpinned, fallbacks []Candidate

	for _, candidate := range candidates {
		switch {
		case len(candidate.Matchers) > 0:
			if matchesAll(document, candidate.Matchers) {
				pinned = append(pinned, candidate)
			}
		case candidate.Fallback:
			fallbacks = append(fallbacks, candidate)
		default:
			unpinned = append(unpinned, candidate)
		}
	}

	if len(pinned) > 0 {
		return decodeOne(data, pinned)
	}

	name, payload, err := decodeOne(data, unpinned)
	if !errors.Is(err, ErrUnknownMessage) || len(fallbacks) == 0 {
		return name, payload, err
	}

	return decodeOne(data, fallbacks)
}

func decodeOne(data []byte, candidates []Candidate) (string, any, error) {
	var (
		matchedName    string
		matchedPayload any
		matchedNames   []string
		failures       = []error{ErrUnknownMessage}
	)

	for _, candidate := range candidates {
		payload, err := candidate.Decode(data)
		if err != nil {
			failures = append(failures, ctxerrors.Wrap(err, candidate.Name))

			continue
		}

		matchedName = candidate.Name
		matchedPayload = payload

		matchedNames = append(matchedNames, candidate.Name)
	}

	switch len(matchedNames) {
	case 1:
		return matchedName, matchedPayload, nil
	case 0:
		// Joined so callers can errors.Is both ErrUnknownMessage and the reason
		// each candidate rejected the message.
		return "", nil, ctxerrors.Wrapf(
			errors.Join(failures...),
			"tried %d candidates",
			len(candidates),
		)
	default:
		return "", nil, ctxerrors.Wrapf(
			ErrAmbiguousMessage,
			"matches %s",
			strings.Join(matchedNames, ", "),
		)
	}
}

func matchesAll(document any, matchers []Matcher) bool {
	for _, matcher := range matchers {
		if !PointerEquals(document, matcher.Pointer, matcher.Value) {
			return false
		}
	}

	return true
}

// PointerEquals reports whether the value at pointer inside document equals
// the JSON text expected. Numbers compare by value.
func PointerEquals(document any, pointer, expected string) bool {
	actual, ok := pointerValue(document, pointer)
	if !ok {
		return false
	}

	var want any
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		return false
	}

	return reflect.DeepEqual(actual, want)
}

func pointerValue(document any, pointer string) (any, bool) {
	if pointer == "" {
		return document, true
	}

	current := document

	for token := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(
			strings.ReplaceAll(token, "~1", "/"),
			"~0",
			"~",
		)

		switch typed := current.(type) {
		case map[string]any:
			value, ok := typed[token]
			if !ok {
				return nil, false
			}

			current = value
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}

			current = typed[index]
		default:
			return nil, false
		}
	}

	return current, true
}

// DiscriminatorValue returns the discriminator property of a union value: the
// string itself for strings, the raw JSON text otherwise.
func DiscriminatorValue(
	data []byte,
	typeName,
	property string,
) (string, error) {
	members, err := DecodeObject(data, typeName)
	if err != nil {
		return "", err
	}

	raw, ok := members[property]
	if !ok {
		return "", ctxerrors.Wrapf(
			commerr.ErrRequiredFieldNotSet,
			"%s.%s",
			typeName,
			property,
		)
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	return string(bytes.TrimSpace(raw)), nil
}

// SingleMatch picks the one variant a union without a discriminator decoded
// as.
func SingleMatch(typeName string, matches []any) (any, error) {
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, ctxerrors.Wrap(ErrNoVariantMatched, typeName)
	default:
		return nil, ctxerrors.Wrapf(
			ErrAmbiguousVariant,
			"%s: %d variants match",
			typeName,
			len(matches),
		)
	}
}

// UnmarshalStrict decodes data into target and runs its Validate method.
func UnmarshalStrict(data []byte, target any) error {
	if err := json.Unmarshal(data, target); err != nil {
		return DecodeError(fmt.Sprintf("%T", target), err)
	}

	return ValidateValue(reflect.ValueOf(target).Elem().Interface())
}

// DecodeError wraps an error from json.Unmarshal. Malformed JSON and JSON of
// the wrong type become commerr.ErrCouldNotDecode; errors returned by nested
// UnmarshalJSON methods keep their chain, so a validation failure deep inside
// a message still matches commerr.ErrValidationFailed.
func DecodeError(typeName string, err error) error {
	var (
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
	)

	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return ctxerrors.Wrapf(
			commerr.ErrCouldNotDecode,
			"%s: %v",
			typeName,
			err,
		)
	}

	return ctxerrors.Wrap(err, typeName)
}
