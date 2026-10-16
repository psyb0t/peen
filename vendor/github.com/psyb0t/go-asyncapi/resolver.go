package asyncapi

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"gopkg.in/yaml.v3"
)

const (
	refKey             = "$ref"
	maxRefHops         = 64
	jsonPointerPrefix  = "#"
	jsonPointerEscape0 = "~0"
	jsonPointerEscape1 = "~1"
)

// Location identifies a value inside a loaded document by file and JSON
// pointer. Two $refs that resolve to the same Location point at the same
// value, so it works as a map key for deduplicating schemas.
type Location struct {
	File    string
	Pointer string
}

// String renders the location as a JSON reference, file#/pointer.
func (l Location) String() string {
	return l.File + jsonPointerPrefix + l.Pointer
}

// Resolver loads documents and follows $ref values between and inside them.
type Resolver struct {
	documents map[string]any
}

// NewResolver returns a resolver with no documents loaded.
func NewResolver() *Resolver {
	return &Resolver{documents: map[string]any{}}
}

// Document returns the root value of the document at path, loading and caching
// it on first use. Both JSON and YAML documents are accepted.
func (r *Resolver) Document(path string) (any, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, ctxerrors.Wrapf(err, "resolve path %s", path)
	}

	if document, ok := r.documents[absolute]; ok {
		return document, nil
	}

	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, ctxerrors.Wrapf(err, "read spec %s", absolute)
	}

	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, ctxerrors.Wrapf(
			commerr.ErrParseFailed,
			"decode spec %s: %v",
			absolute,
			err,
		)
	}

	document, err := fromNode(&node)
	if err != nil {
		return nil, ctxerrors.Wrapf(err, "convert spec %s", absolute)
	}

	r.documents[absolute] = document

	return document, nil
}

// Deref follows $ref chains starting at value, which lives at location. It
// returns the first value that is not a reference and where that value lives.
func (r *Resolver) Deref(value any, location Location) (any, Location, error) {
	for range maxRefHops {
		object, ok := value.(*Object)
		if !ok {
			return value, location, nil
		}

		ref, isRef := object.Get(refKey)
		if !isRef {
			return value, location, nil
		}

		refText, ok := ref.(string)
		if !ok {
			return nil, location, ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"$ref at %s is not a string",
				location,
			)
		}

		target, err := r.refLocation(refText, location)
		if err != nil {
			return nil, location, err
		}

		resolved, err := r.Lookup(target)
		if err != nil {
			return nil, location, ctxerrors.Wrapf(
				err,
				"follow $ref %q at %s",
				refText,
				location,
			)
		}

		value = resolved
		location = target
	}

	return nil, location, ctxerrors.Wrapf(
		commerr.ErrInvalidValue,
		"$ref chain longer than %d hops at %s",
		maxRefHops,
		location,
	)
}

// Lookup returns the raw value at location without following references.
func (r *Resolver) Lookup(location Location) (any, error) {
	document, err := r.Document(location.File)
	if err != nil {
		return nil, err
	}

	current := document
	for _, token := range pointerTokens(location.Pointer) {
		current, err = step(current, token)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "resolve %s", location)
		}
	}

	return current, nil
}

func (r *Resolver) refLocation(ref string, from Location) (Location, error) {
	filePart, pointer, _ := strings.Cut(ref, jsonPointerPrefix)
	if strings.Contains(filePart, "://") {
		return Location{}, ctxerrors.Wrapf(
			commerr.ErrInvalidArgument,
			//nolint:lll // one error message, splitting it changes the text
			"remote $ref %q is not supported, download the document and reference it by path",
			ref,
		)
	}

	file := from.File

	switch {
	case filePart == "":
	case filepath.IsAbs(filePart):
		file = filePart
	default:
		file = filepath.Join(filepath.Dir(from.File), filePart)
	}

	absolute, err := filepath.Abs(file)
	if err != nil {
		return Location{}, ctxerrors.Wrapf(err, "resolve $ref file %s", file)
	}

	return Location{File: absolute, Pointer: pointer}, nil
}

// Child returns the location of key inside location.
func (l Location) Child(key string) Location {
	escaped := strings.ReplaceAll(key, "~", jsonPointerEscape0)
	escaped = strings.ReplaceAll(escaped, "/", jsonPointerEscape1)

	return Location{File: l.File, Pointer: l.Pointer + "/" + escaped}
}

// LastToken returns the unescaped final JSON pointer token, or "" at the root.
func (l Location) LastToken() string {
	tokens := pointerTokens(l.Pointer)
	if len(tokens) == 0 {
		return ""
	}

	return tokens[len(tokens)-1]
}

func pointerTokens(pointer string) []string {
	trimmed := strings.TrimPrefix(pointer, "/")
	if trimmed == "" {
		return nil
	}

	tokens := strings.Split(trimmed, "/")
	for i, token := range tokens {
		token = strings.ReplaceAll(token, jsonPointerEscape1, "/")
		tokens[i] = strings.ReplaceAll(token, jsonPointerEscape0, "~")
	}

	return tokens
}

func step(current any, token string) (any, error) {
	switch typed := current.(type) {
	case *Object:
		value, ok := typed.Get(token)
		if !ok {
			return nil, ctxerrors.Wrapf(commerr.ErrNotFound, "key %q", token)
		}

		return value, nil
	case []any:
		index, err := strconv.Atoi(token)
		if err != nil || index < 0 || index >= len(typed) {
			return nil, ctxerrors.Wrapf(commerr.ErrNotFound, "index %q", token)
		}

		return typed[index], nil
	default:
		return nil, ctxerrors.Wrapf(
			commerr.ErrNotFound,
			"token %q in a scalar",
			token,
		)
	}
}
