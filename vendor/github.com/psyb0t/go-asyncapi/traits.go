package asyncapi

import (
	"maps"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const traitsKey = "traits"

// Located is a value merged in from a trait, together with where it was
// defined, so $refs inside it still resolve against the trait's document.
type Located struct {
	Value any
	At    Location
}

// applyTraits merges the traits listed on target into a copy of target,
// following the AsyncAPI traits merge mechanism: JSON Merge Patch in list
// order, where a property already on the target is never overridden.
// Top-level values that come from a trait are wrapped in Located.
func (r *Resolver) applyTraits(target *Object, at Location) (*Object, error) {
	traits, ok := target.Values[traitsKey].([]any)
	if !ok || len(traits) == 0 {
		return target, nil
	}

	merged := copyObject(target)

	for i, trait := range traits {
		resolved, traitAt, err := r.Deref(
			trait,
			at.Child(traitsKey).Child(strconv.Itoa(i)),
		)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "trait %d", i)
		}

		traitObject, isObject := resolved.(*Object)
		if !isObject {
			return nil, ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"trait at %s is not a mapping",
				traitAt,
			)
		}

		for _, key := range traitObject.Keys {
			existing, exists := merged.Values[key]
			incoming := traitObject.Values[key]

			if !exists {
				merged.Keys = append(merged.Keys, key)
				merged.Values[key] = Located{
					Value: incoming,
					At:    traitAt.Child(key),
				}

				continue
			}

			existingValue := unwrapLocated(existing)
			existingObject, existingIsObject := existingValue.(*Object)

			incomingObject, incomingIsObject := incoming.(*Object)
			if existingIsObject && incomingIsObject {
				merged.Values[key] = mergeKeepTarget(
					existingObject,
					incomingObject,
				)
			}
		}
	}

	return merged, nil
}

// mergeKeepTarget is JSON Merge Patch with the target winning every conflict:
// keys only the patch has are added, nested mappings merge recursively.
func mergeKeepTarget(target, patch *Object) *Object {
	merged := copyObject(target)

	for _, key := range patch.Keys {
		existing, exists := merged.Values[key]
		if !exists {
			merged.Keys = append(merged.Keys, key)
			merged.Values[key] = patch.Values[key]

			continue
		}

		existingObject, existingIsObject := existing.(*Object)
		patchObject, patchIsObject := patch.Values[key].(*Object)

		if existingIsObject && patchIsObject {
			merged.Values[key] = mergeKeepTarget(existingObject, patchObject)
		}
	}

	return merged
}

func copyObject(source *Object) *Object {
	copied := &Object{
		Keys:   append([]string(nil), source.Keys...),
		Values: make(map[string]any, len(source.Values)),
	}

	maps.Copy(copied.Values, source.Values)

	return copied
}

func unwrapLocated(value any) any {
	if located, ok := value.(Located); ok {
		return located.Value
	}

	return value
}

// field returns the value under key and where it lives: inside base for the
// object's own properties, at the trait for merged-in ones.
func field(object *Object, key string, base Location) (any, Location) {
	value := object.Values[key]
	if located, ok := value.(Located); ok {
		return located.Value, located.At
	}

	return value, base.Child(key)
}

// fieldString is field for string properties.
func fieldString(object *Object, key string) string {
	text, _ := unwrapLocated(object.Values[key]).(string)

	return text
}

// isJSONSchemaFormat reports whether a Multi Format Schema Object's
// schemaFormat is JSON Schema, an AsyncAPI schema or an OpenAPI schema, the
// formats the generator reads. Avro, RAML, Protobuf and others are not.
func isJSONSchemaFormat(schemaFormat string) bool {
	format := strings.ToLower(schemaFormat)

	return format == "" ||
		strings.HasPrefix(format, "application/vnd.aai.asyncapi") ||
		strings.HasPrefix(format, "application/schema+json") ||
		strings.HasPrefix(format, "application/schema+yaml") ||
		strings.HasPrefix(format, "application/vnd.oai.openapi")
}
