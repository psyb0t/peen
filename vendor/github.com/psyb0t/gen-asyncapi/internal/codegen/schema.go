package codegen

import (
	"strings"

	asyncapi "github.com/psyb0t/go-asyncapi"
)

const (
	jsonTypeString  = "string"
	jsonTypeInteger = "integer"
	jsonTypeNumber  = "number"
	jsonTypeBoolean = "boolean"
	jsonTypeArray   = "array"
	jsonTypeObject  = "object"
	jsonTypeNull    = "null"

	formatDateTime = "date-time"
	formatInt32    = "int32"
	formatFloat    = "float"
)

// schemaInfo is the JSON Schema keywords the generator understands, read once
// from a dereferenced schema object.
type schemaInfo struct {
	object *asyncapi.Object

	types       []string
	nullable    bool
	title       string
	description string
	format      string

	enum     []any
	hasConst bool
	constant any

	properties           *asyncapi.Object
	required             map[string]bool
	additionalProperties any
	hasAdditional        bool

	items any

	oneOf []any
	anyOf []any
	allOf []any

	discriminatorProperty string
	discriminatorMapping  *asyncapi.Object

	minimum          any
	maximum          any
	exclusiveMinimum any
	exclusiveMaximum any
	minLength        any
	maxLength        any
	pattern          string
	minItems         any
	maxItems         any

	contentMediaType string
	contentSchema    any

	multipleOf        any
	uniqueItems       bool
	minProperties     any
	maxProperties     any
	patternProperties *asyncapi.Object
	propertyNames     *asyncapi.Object
	dependentRequired map[string][]string

	deprecated bool
	hasDefault bool
	defaultVal any
	examples   []any

	// unenforced lists keywords present on the schema that the generated code
	// does not check, so the doc comment can say so.
	unenforced []string
}

// isCheckedFormat reports whether a format changes the generated Go type, so
// decoding enforces it. Every other format is named in the doc comment.
func isCheckedFormat(format string) bool {
	switch format {
	case formatDateTime, formatInt32, formatFloat, "int64", "double":
		return true
	default:
		return false
	}
}

// unenforcedKeywords are JSON Schema keywords the generator reads past. Each
// one that appears on a schema is named in the generated doc comment.
func unenforcedKeywords() []string {
	return []string{
		"not",
		"if",
		"then",
		"else",
		"contains",
		"minContains",
		"maxContains",
		"prefixItems",
		"unevaluatedProperties",
		"unevaluatedItems",
		"dependentSchemas",
	}
}

func readSchema(object *asyncapi.Object) *schemaInfo {
	info := &schemaInfo{
		object:      object,
		title:       object.String("title"),
		description: object.String("description"),
		format:      object.String("format"),
		required:    map[string]bool{},
		pattern:     object.String("pattern"),

		contentMediaType: object.String("contentMediaType"),
	}

	readTypeKeywords(info, object)
	readStructureKeywords(info, object)
	readConstraintKeywords(info, object)
	readAnnotationKeywords(info, object)
	info.unenforced = unenforcedFor(info, object)

	return info
}

// readTypeKeywords reads type, nullable, enum and const.
func readTypeKeywords(info *schemaInfo, object *asyncapi.Object) {
	switch typed := object.Values["type"].(type) {
	case string:
		info.types = []string{typed}
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				info.types = append(info.types, text)
			}
		}
	}

	if nullable, ok := object.Values["nullable"].(bool); ok && nullable {
		info.nullable = true
	}

	info.types = withoutNull(info.types, &info.nullable)

	if values, ok := object.Values["enum"].([]any); ok {
		info.enum = withoutNilValues(values, &info.nullable)
	}

	info.constant, info.hasConst = object.Get("const")
}

// readStructureKeywords reads properties, required, items, the composition
// keywords and the discriminator.
func readStructureKeywords(info *schemaInfo, object *asyncapi.Object) {
	info.properties = object.Object("properties")

	if requiredNames, ok := object.Values["required"].([]any); ok {
		for _, name := range requiredNames {
			if text, isText := name.(string); isText {
				info.required[text] = true
			}
		}
	}

	info.additionalProperties, info.hasAdditional = object.Get(
		"additionalProperties",
	)
	info.items = object.Values["items"]
	info.oneOf, _ = object.Values["oneOf"].([]any)
	info.anyOf, _ = object.Values["anyOf"].([]any)
	info.allOf, _ = object.Values["allOf"].([]any)

	switch discriminator := object.Values["discriminator"].(type) {
	case string:
		info.discriminatorProperty = discriminator
	case *asyncapi.Object:
		info.discriminatorProperty = discriminator.String("propertyName")
		info.discriminatorMapping = discriminator.Object("mapping")
	}
}

// readConstraintKeywords reads the validation keywords the generated code
// checks.
func readConstraintKeywords(info *schemaInfo, object *asyncapi.Object) {
	info.minimum = object.Values["minimum"]
	info.maximum = object.Values["maximum"]
	info.exclusiveMinimum = object.Values["exclusiveMinimum"]
	info.exclusiveMaximum = object.Values["exclusiveMaximum"]
	info.minLength = object.Values["minLength"]
	info.maxLength = object.Values["maxLength"]
	info.minItems = object.Values["minItems"]
	info.maxItems = object.Values["maxItems"]
	info.contentSchema = object.Values["contentSchema"]

	info.multipleOf = object.Values["multipleOf"]
	info.uniqueItems, _ = object.Values["uniqueItems"].(bool)
	info.minProperties = object.Values["minProperties"]
	info.maxProperties = object.Values["maxProperties"]
	info.patternProperties = object.Object("patternProperties")
	info.propertyNames = object.Object("propertyNames")
	info.dependentRequired = readDependentRequired(object)
}

// readAnnotationKeywords reads deprecated, default and examples.
func readAnnotationKeywords(info *schemaInfo, object *asyncapi.Object) {
	info.deprecated, _ = object.Values["deprecated"].(bool)
	info.defaultVal, info.hasDefault = object.Get("default")
	info.examples, _ = object.Values["examples"].([]any)
}

// unenforcedFor lists the keywords on a schema that the generated code does
// not check.
func unenforcedFor(info *schemaInfo, object *asyncapi.Object) []string {
	var unenforced []string

	for _, keyword := range unenforcedKeywords() {
		if _, present := object.Get(keyword); present {
			unenforced = append(unenforced, keyword)
		}
	}

	if _, isTuple := object.Values["items"].([]any); isTuple {
		unenforced = append(unenforced, "items (tuple form)")
	}

	if len(info.types) > 1 {
		unenforced = append(
			unenforced,
			"type "+strings.Join(info.types, " or ")+" (kept as raw JSON)",
		)
	}

	if info.format != "" && !isCheckedFormat(info.format) {
		unenforced = append(unenforced, "format "+info.format)
	}

	return unenforced
}

// readDependentRequired reads dependentRequired, and the draft 7
// "dependencies" keyword in its property-list form.
func readDependentRequired(object *asyncapi.Object) map[string][]string {
	result := map[string][]string{}

	for _, keyword := range []string{"dependentRequired", "dependencies"} {
		dependencies := object.Object(keyword)
		if dependencies == nil {
			continue
		}

		for _, property := range dependencies.Keys {
			required, isList := dependencies.Values[property].([]any)
			if !isList {
				continue
			}

			for _, name := range required {
				if text, ok := name.(string); ok {
					result[property] = append(result[property], text)
				}
			}
		}
	}

	return result
}

func withoutNull(types []string, nullable *bool) []string {
	result := make([]string, 0, len(types))
	for _, typeName := range types {
		if typeName == jsonTypeNull {
			*nullable = true

			continue
		}

		result = append(result, typeName)
	}

	return result
}

func withoutNilValues(values []any, nullable *bool) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		if value == nil {
			*nullable = true

			continue
		}

		result = append(result, value)
	}

	return result
}

// primaryType returns the single non-null JSON type, inferring "object" from
// properties and "array" from items when type is omitted. It returns "" when
// the schema allows several types or says nothing about its type.
func (s *schemaInfo) primaryType() string {
	if len(s.types) == 1 {
		return s.types[0]
	}

	if len(s.types) > 1 {
		return ""
	}

	switch {
	case s.properties != nil:
		return jsonTypeObject
	case s.items != nil:
		return jsonTypeArray
	case len(s.enum) > 0:
		return jsonTypeOfValue(s.enum[0])
	case s.hasConst:
		return jsonTypeOfValue(s.constant)
	default:
		return ""
	}
}

// constValue returns the single value a schema allows, from const or from a
// one-element enum.
func (s *schemaInfo) constValue() (any, bool) {
	if s.hasConst {
		return s.constant, true
	}

	if len(s.enum) == 1 {
		return s.enum[0], true
	}

	return nil, false
}

func jsonTypeOfValue(value any) string {
	switch value.(type) {
	case string:
		return jsonTypeString
	case int64:
		return jsonTypeInteger
	case float64:
		return jsonTypeNumber
	case bool:
		return jsonTypeBoolean
	default:
		return ""
	}
}
