package codegen

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	asyncapi "github.com/psyb0t/go-asyncapi"
)

const (
	componentSchemasPrefix = "/components/schemas/"

	goTypeString    = "string"
	goTypeInt64     = "int64"
	goTypeInt32     = "int32"
	goTypeFloat64   = "float64"
	goTypeFloat32   = "float32"
	goTypeBool      = "bool"
	goTypeTime      = "time.Time"
	goTypeRawJSON   = "json.RawMessage"
	goTypeAnyMap    = "map[string]any"
	variantFallback = "Variant"
)

// builder turns schemas into Go declarations. Each schema location becomes at
// most one named type, so every $ref to it shares that type.
type builder struct {
	resolver *asyncapi.Resolver
	names    *nameSet
	decls    []*decl
	byPlace  map[asyncapi.Location]*typeRef
	usesTime bool
}

func newBuilder(resolver *asyncapi.Resolver, reserved ...string) *builder {
	return &builder{
		resolver: resolver,
		names:    newNameSet(reserved...),
		byPlace:  map[asyncapi.Location]*typeRef{},
	}
}

// typeOf returns the Go type for the schema value found at location. hint
// names the type when the schema is inline and needs its own declaration.
func (b *builder) typeOf(
	value any,
	at asyncapi.Location,
	hint string,
) (*typeRef, error) {
	resolved, location, err := b.resolver.Deref(value, at)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "dereference schema")
	}

	if cached, ok := b.byPlace[location]; ok {
		return cached, nil
	}

	object, isObject := resolved.(*asyncapi.Object)
	if !isObject {
		if allowed, isBool := resolved.(bool); isBool && !allowed {
			return nil, ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"schema at %s allows nothing",
				location,
			)
		}

		return &typeRef{kind: refAny, expr: goTypeRawJSON}, nil
	}

	if componentName, ok := componentSchemaName(location); ok {
		hint = GoName(componentName)
	}

	info := readSchema(object)
	if info.title != "" && !isComponentSchema(location) {
		hint = GoName(info.title)
	}

	return b.build(info, location, hint)
}

func (b *builder) build(
	info *schemaInfo,
	location asyncapi.Location,
	hint string,
) (*typeRef, error) {
	switch {
	case len(info.allOf) > 0:
		return b.allOfStruct(info, location, hint)
	case len(info.oneOf) > 0:
		return b.union(
			info,
			info.oneOf,
			location.Child("oneOf"),
			location,
			hint,
		)
	case len(info.anyOf) > 0:
		return b.union(
			info,
			info.anyOf,
			location.Child("anyOf"),
			location,
			hint,
		)
	case len(info.enum) > 1:
		return b.enum(info, location, hint)
	}

	switch info.primaryType() {
	case jsonTypeObject:
		if info.properties != nil {
			return b.object(info, location, hint)
		}

		return b.mapType(info, location, hint)
	case jsonTypeArray:
		return b.array(info, location, hint)
	case jsonTypeString, jsonTypeInteger, jsonTypeNumber, jsonTypeBoolean:
		return b.componentAlias(location, hint, b.primitive(info))
	default:
		return b.componentAlias(
			location,
			hint,
			&typeRef{kind: refAny, expr: goTypeRawJSON},
		)
	}
}

// componentAlias gives a components.schemas primitive, array or map its own
// type alias, so the spec's name appears in the generated API. Inline schemas
// are used as they are.
func (b *builder) componentAlias(
	location asyncapi.Location,
	hint string,
	ref *typeRef,
) (*typeRef, error) {
	if !isComponentSchema(location) {
		return ref, nil
	}

	aliasDecl := &decl{
		kind:  declAlias,
		name:  b.names.claim(hint),
		alias: ref,
	}
	named := &typeRef{
		kind:     refNamed,
		expr:     aliasDecl.name,
		decl:     aliasDecl,
		jsonType: ref.jsonType,
	}
	b.byPlace[location] = named
	b.decls = append(b.decls, aliasDecl)

	return named, nil
}

func (b *builder) primitive(info *schemaInfo) *typeRef {
	ref := &typeRef{kind: refPrimitive, jsonType: info.primaryType()}

	switch ref.jsonType {
	case jsonTypeString:
		ref.expr = goTypeString
		if info.format == formatDateTime {
			ref.expr = goTypeTime
			ref.isTime = true
			b.usesTime = true

			return ref
		}

		ref.check = appendIntCheck(ref.check, checkMinLength, info.minLength)
		ref.check = appendIntCheck(ref.check, checkMaxLength, info.maxLength)

		if info.pattern != "" {
			ref.check = append(
				ref.check,
				check{kind: checkPattern, arg: strconv.Quote(info.pattern)},
			)
		}
	case jsonTypeInteger:
		ref.expr = goTypeInt64
		if info.format == formatInt32 {
			ref.expr = goTypeInt32
		}

		ref.check = appendNumericChecks(ref.check, info)
	case jsonTypeNumber:
		ref.expr = goTypeFloat64
		if info.format == formatFloat {
			ref.expr = goTypeFloat32
		}

		ref.check = appendNumericChecks(ref.check, info)
	case jsonTypeBoolean:
		ref.expr = goTypeBool
	}

	return ref
}

func appendNumericChecks(checks []check, info *schemaInfo) []check {
	checks = appendBound(
		checks,
		info.minimum,
		info.exclusiveMinimum,
		checkMinimum,
		checkExclusiveMinimum,
	)
	checks = appendBound(
		checks,
		info.maximum,
		info.exclusiveMaximum,
		checkMaximum,
		checkExclusiveMaximum,
	)

	return appendNumberCheck(checks, checkMultipleOf, info.multipleOf)
}

// appendBound adds one side of a numeric range. JSON Schema draft 4 marks an
// exclusive bound with a boolean next to minimum/maximum; later drafts give the
// exclusive bound as its own number.
func appendBound(
	checks []check,
	inclusive,
	exclusive any,
	inclusiveKind,
	exclusiveKind checkKind,
) []check {
	if isExclusive, isDraft4 := exclusive.(bool); isDraft4 {
		if isExclusive {
			return appendNumberCheck(checks, exclusiveKind, inclusive)
		}

		return appendNumberCheck(checks, inclusiveKind, inclusive)
	}

	checks = appendNumberCheck(checks, inclusiveKind, inclusive)

	return appendNumberCheck(checks, exclusiveKind, exclusive)
}

func appendIntCheck(checks []check, kind checkKind, value any) []check {
	number, ok := value.(int64)
	if !ok {
		return checks
	}

	return append(checks, check{kind: kind, arg: strconv.FormatInt(number, 10)})
}

func appendNumberCheck(checks []check, kind checkKind, value any) []check {
	switch number := value.(type) {
	case int64:
		return append(
			checks,
			check{kind: kind, arg: strconv.FormatInt(number, 10)},
		)
	case float64:
		return append(
			checks,
			check{kind: kind, arg: strconv.FormatFloat(number, 'g', -1, 64)},
		)
	default:
		return checks
	}
}

func (b *builder) array(
	info *schemaInfo,
	location asyncapi.Location,
	hint string,
) (*typeRef, error) {
	elem, err := b.typeOf(info.items, location.Child("items"), hint+"Item")
	if err != nil {
		return nil, err
	}

	ref := &typeRef{kind: refArray, expr: "[]" + elem.expr, elem: elem}
	ref.check = appendIntCheck(ref.check, checkMinItems, info.minItems)
	ref.check = appendIntCheck(ref.check, checkMaxItems, info.maxItems)

	if info.uniqueItems {
		ref.check = append(ref.check, check{kind: checkUniqueItems})
	}

	return b.componentAlias(location, hint, ref)
}

// mapType handles an object without fixed properties. Values take their type
// from additionalProperties, or from the single patternProperties schema;
// patternProperties and propertyNames patterns are checked on the keys.
func (b *builder) mapType(
	info *schemaInfo,
	location asyncapi.Location,
	hint string,
) (*typeRef, error) {
	valueSchema, valueAt := b.mapValueSchema(info, location)

	ref := &typeRef{kind: refAny, expr: goTypeAnyMap}

	if valueSchema != nil {
		elem, err := b.typeOf(valueSchema, valueAt, hint+"Value")
		if err != nil {
			return nil, err
		}

		ref = &typeRef{
			kind: refMap,
			expr: "map[string]" + elem.expr,
			elem: elem,
		}
	}

	ref.check = appendIntCheck(
		ref.check,
		checkMinProperties,
		info.minProperties,
	)
	ref.check = appendIntCheck(
		ref.check,
		checkMaxProperties,
		info.maxProperties,
	)

	if ref.kind == refMap {
		ref.check = append(ref.check, keyPatternChecks(info)...)
	}

	return b.componentAlias(location, hint, ref)
}

func (b *builder) mapValueSchema(
	info *schemaInfo,
	location asyncapi.Location,
) (any, asyncapi.Location) {
	additional, isSchema := info.additionalProperties.(*asyncapi.Object)
	if isSchema {
		return additional, location.Child("additionalProperties")
	}

	if hasSinglePatternProperty(info) {
		pattern := info.patternProperties.Keys[0]
		patternAt := location.Child("patternProperties").Child(pattern)

		return info.patternProperties.Values[pattern], patternAt
	}

	return nil, location
}

// hasSinglePatternProperty reports whether patternProperties holds exactly one
// pattern, the case where the pattern can type the map values and its keys.
func hasSinglePatternProperty(info *schemaInfo) bool {
	return info.patternProperties != nil &&
		len(info.patternProperties.Keys) == 1
}

func keyPatternChecks(info *schemaInfo) []check {
	var checks []check

	if hasSinglePatternProperty(info) && !info.hasAdditional {
		checks = append(
			checks,
			check{
				kind: checkKeyPattern,
				arg:  strconv.Quote(info.patternProperties.Keys[0]),
			},
		)
	}

	if info.propertyNames != nil {
		if pattern := info.propertyNames.String("pattern"); pattern != "" {
			checks = append(
				checks,
				check{kind: checkKeyPattern, arg: strconv.Quote(pattern)},
			)
		}
	}

	return checks
}

// isEnumBase reports whether a primitive can be the base of a named enum type.
// Times, booleans and untyped values keep their plain type.
func isEnumBase(base *typeRef) bool {
	return base.expr != goTypeTime &&
		base.jsonType != jsonTypeBoolean &&
		base.jsonType != ""
}

func (b *builder) enum(
	info *schemaInfo,
	location asyncapi.Location,
	hint string,
) (*typeRef, error) {
	base := b.primitive(info)
	if !isEnumBase(base) {
		return base, nil
	}

	enumDecl := &decl{
		kind:     declEnum,
		name:     b.names.claim(hint),
		doc:      schemaDoc(info),
		enumBase: base.expr,
	}

	valueNames := newNameSet()

	for _, value := range info.enum {
		literal, ok := goLiteral(value)
		if !ok {
			continue
		}

		label := literalLabel(value)
		if _, isText := value.(string); isText {
			label = GoName(label)
		}

		enumDecl.enumValues = append(enumDecl.enumValues, enumValue{
			goName:  valueNames.claim(enumDecl.name + label),
			literal: literal,
		})
	}

	ref := &typeRef{
		kind:     refNamed,
		expr:     enumDecl.name,
		decl:     enumDecl,
		jsonType: base.jsonType,
	}
	b.byPlace[location] = ref
	b.decls = append(b.decls, enumDecl)

	return ref, nil
}

func (b *builder) object(
	info *schemaInfo,
	location asyncapi.Location,
	hint string,
) (*typeRef, error) {
	structDecl := &decl{
		kind: declStruct,
		name: b.names.claim(hint),
		doc:  schemaDoc(info),
	}
	applyObjectConstraints(structDecl, info)
	ref := &typeRef{
		kind:     refNamed,
		expr:     structDecl.name,
		decl:     structDecl,
		jsonType: jsonTypeObject,
	}

	// Registered before the fields are built, so a schema that refers to itself
	// resolves to this type instead of recursing forever.
	b.byPlace[location] = ref
	b.decls = append(b.decls, structDecl)

	if allowed, ok := info.additionalProperties.(bool); ok && !allowed {
		structDecl.strictKeys = true
	}

	if err := b.addFields(structDecl, info, location); err != nil {
		return nil, err
	}

	return ref, nil
}

// applyObjectConstraints records the object-level keywords the generated
// UnmarshalJSON checks: property counts and dependentRequired.
func applyObjectConstraints(structDecl *decl, info *schemaInfo) {
	if count, ok := info.minProperties.(int64); ok {
		structDecl.minProperties = strconv.FormatInt(count, 10)
	}

	if count, ok := info.maxProperties.(int64); ok {
		structDecl.maxProperties = strconv.FormatInt(count, 10)
	}

	properties := make([]string, 0, len(info.dependentRequired))
	for property := range info.dependentRequired {
		properties = append(properties, property)
	}

	slices.Sort(properties)

	for _, property := range properties {
		structDecl.dependents = append(structDecl.dependents, dependent{
			property: property,
			requires: info.dependentRequired[property],
		})
	}
}

func (b *builder) addFields(
	structDecl *decl,
	info *schemaInfo,
	location asyncapi.Location,
) error {
	if info.properties == nil {
		return nil
	}

	fieldNames := newNameSet()
	base := location.Child("properties")

	for _, jsonName := range info.properties.Keys {
		propertyValue := info.properties.Values[jsonName]
		propertyAt := base.Child(jsonName)

		newField, err := b.field(
			structDecl.name,
			jsonName,
			propertyValue,
			propertyAt,
			info.required[jsonName],
		)
		if err != nil {
			return ctxerrors.Wrapf(
				err,
				"property %s of %s",
				jsonName,
				structDecl.name,
			)
		}

		newField.goName = fieldNames.claim(GoName(jsonName))
		structDecl.fields = append(structDecl.fields, newField)
	}

	return nil
}

func (b *builder) field(
	parentName, jsonName string,
	value any,
	at asyncapi.Location,
	required bool,
) (*field, error) {
	resolved, resolvedAt, err := b.resolver.Deref(value, at)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "dereference property")
	}

	newField := &field{jsonName: jsonName, required: required}

	propertyObject, isObject := resolved.(*asyncapi.Object)
	if isObject {
		info := readSchema(propertyObject)
		newField.doc = schemaDoc(info)

		if constant, ok := info.constValue(); ok {
			return b.constField(newField, info, constant)
		}

		if err := b.contentField(
			newField,
			info,
			resolvedAt,
			parentName,
		); err != nil {
			return nil, err
		}

		nullable := info.nullable
		newField.nullable = nullable

		newField.ref, err = b.typeOf(value, at, parentName+GoName(jsonName))
		if err != nil {
			return nil, err
		}

		applyPresence(newField, nullable)

		return newField, nil
	}

	newField.ref, err = b.typeOf(value, at, parentName+GoName(jsonName))
	if err != nil {
		return nil, err
	}

	applyPresence(newField, false)

	return newField, nil
}

// applyPresence picks how a field shows that it is absent or null: a pointer,
// or the nil zero value of slices, maps and raw JSON.
func applyPresence(target *field, nullable bool) {
	switch {
	case target.required && !nullable:
		return
	case target.ref.isNillable():
		target.omit = !target.required
	default:
		target.pointer = true
		target.omit = !target.required
	}
}

func (b *builder) constField(
	target *field,
	info *schemaInfo,
	constant any,
) (*field, error) {
	literal, ok := goLiteral(constant)
	if !ok {
		return nil, ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"const %v is not a scalar",
			constant,
		)
	}

	constJSON, err := json.Marshal(constant)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode const")
	}

	target.ref = b.primitive(&schemaInfo{
		types:  []string{jsonTypeOfValue(constant)},
		format: info.format,
	})
	target.constLiteral = literal
	target.constJSON = string(constJSON)

	return target, nil
}

func (b *builder) contentField(
	target *field,
	info *schemaInfo,
	at asyncapi.Location,
	parentName string,
) error {
	hasJSONMediaType := info.contentMediaType == "" ||
		strings.HasSuffix(info.contentMediaType, "json")

	isJSONString := info.primaryType() == jsonTypeString &&
		info.contentSchema != nil &&
		hasJSONMediaType
	if !isJSONString {
		return nil
	}

	content, err := b.typeOf(
		info.contentSchema,
		at.Child("contentSchema"),
		parentName+GoName(target.jsonName)+"Content",
	)
	if err != nil {
		return ctxerrors.Wrap(err, "contentSchema")
	}

	target.content = content

	return nil
}

func (b *builder) allOfStruct(
	info *schemaInfo,
	location asyncapi.Location,
	hint string,
) (*typeRef, error) {
	structDecl := &decl{
		kind: declStruct,
		name: b.names.claim(hint),
		doc:  schemaDoc(info),
	}
	ref := &typeRef{
		kind:     refNamed,
		expr:     structDecl.name,
		decl:     structDecl,
		jsonType: jsonTypeObject,
	}
	b.byPlace[location] = ref
	b.decls = append(b.decls, structDecl)

	merged := &schemaInfo{
		properties:        &asyncapi.Object{Values: map[string]any{}},
		required:          map[string]bool{},
		dependentRequired: map[string][]string{},
	}

	members := append([]any{}, info.allOf...)
	if info.properties != nil {
		members = append(members, info.object)
	}

	for i, member := range members {
		memberAt := location.Child("allOf").Child(strconv.Itoa(i))
		if i >= len(info.allOf) {
			memberAt = location
		}

		if err := b.mergeMember(merged, member, memberAt); err != nil {
			return nil, err
		}
	}

	applyObjectConstraints(structDecl, merged)

	if err := b.addMergedFields(structDecl, merged); err != nil {
		return nil, err
	}

	return ref, nil
}

// mergedProperty remembers where a merged property came from, so $refs inside
// it resolve against the right document.
type mergedProperty struct {
	value any
	at    asyncapi.Location
}

func (b *builder) mergeMember(
	merged *schemaInfo,
	member any,
	at asyncapi.Location,
) error {
	resolved, resolvedAt, err := b.resolver.Deref(member, at)
	if err != nil {
		return ctxerrors.Wrap(err, "dereference allOf member")
	}

	object, ok := resolved.(*asyncapi.Object)
	if !ok {
		return nil
	}

	info := readSchema(object)
	for _, nested := range info.allOf {
		if err := b.mergeMember(
			merged,
			nested,
			resolvedAt.Child("allOf"),
		); err != nil {
			return err
		}
	}

	mergeConstraints(merged, info)
	mergeProperties(merged, info, resolvedAt)

	return nil
}

// mergeConstraints folds the object-level keywords of one allOf member into
// merged.
func mergeConstraints(merged, info *schemaInfo) {
	for name := range info.required {
		merged.required[name] = true
	}

	for property, requires := range info.dependentRequired {
		for _, name := range requires {
			if !slices.Contains(merged.dependentRequired[property], name) {
				merged.dependentRequired[property] = append(
					merged.dependentRequired[property],
					name,
				)
			}
		}
	}

	if info.minProperties != nil {
		merged.minProperties = info.minProperties
	}

	if info.maxProperties != nil {
		merged.maxProperties = info.maxProperties
	}

	if allowed, isBool := info.additionalProperties.(bool); isBool && !allowed {
		merged.additionalProperties = false
	}
}

// mergeProperties adds the properties of one allOf member to merged, keeping
// the order in which they first appear.
func mergeProperties(
	merged, info *schemaInfo,
	resolvedAt asyncapi.Location,
) {
	if info.properties == nil {
		return
	}

	for _, name := range info.properties.Keys {
		if _, exists := merged.properties.Values[name]; !exists {
			merged.properties.Keys = append(merged.properties.Keys, name)
		}

		merged.properties.Values[name] = mergedProperty{
			value: info.properties.Values[name],
			at:    resolvedAt.Child("properties").Child(name),
		}
	}
}

func (b *builder) addMergedFields(structDecl *decl, merged *schemaInfo) error {
	if allowed, ok := merged.additionalProperties.(bool); ok && !allowed {
		structDecl.strictKeys = true
	}

	fieldNames := newNameSet()

	for _, jsonName := range merged.properties.Keys {
		property, _ := merged.properties.Values[jsonName].(mergedProperty)

		newField, err := b.field(
			structDecl.name,
			jsonName,
			property.value,
			property.at,
			merged.required[jsonName],
		)
		if err != nil {
			return ctxerrors.Wrapf(
				err,
				"property %s of %s",
				jsonName,
				structDecl.name,
			)
		}

		newField.goName = fieldNames.claim(GoName(jsonName))
		structDecl.fields = append(structDecl.fields, newField)
	}

	return nil
}

func (b *builder) union(
	info *schemaInfo,
	members []any,
	membersAt asyncapi.Location,
	location asyncapi.Location,
	hint string,
) (*typeRef, error) {
	unionDecl := &decl{
		kind: declUnion,
		name: b.names.claim(hint),
		doc:  schemaDoc(info),
	}
	ref := &typeRef{kind: refNamed, expr: unionDecl.name, decl: unionDecl}
	b.byPlace[location] = ref
	b.decls = append(b.decls, unionDecl)

	suffixes := newNameSet()
	locations := make([]asyncapi.Location, 0, len(members))

	for i, member := range members {
		memberAt := membersAt.Child(strconv.Itoa(i))

		_, resolvedAt, err := b.resolver.Deref(member, memberAt)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "dereference union variant")
		}

		variantRef, err := b.typeOf(
			member,
			memberAt,
			unionDecl.name+variantFallback+strconv.Itoa(i+1),
		)
		if err != nil {
			return nil, ctxerrors.Wrapf(
				err,
				"variant %d of %s",
				i+1,
				unionDecl.name,
			)
		}

		unionDecl.variants = append(unionDecl.variants, &variant{
			suffix: suffixes.claim(variantSuffix(variantRef)),
			ref:    variantRef,
		})
		locations = append(locations, resolvedAt)
	}

	if err := b.assignDiscriminator(unionDecl, info, locations); err != nil {
		return nil, err
	}

	return ref, nil
}

func variantSuffix(ref *typeRef) string {
	switch ref.kind {
	case refNamed:
		return ref.decl.name
	case refArray:
		return "ArrayOf" + variantSuffix(ref.elem)
	case refMap:
		return "MapOf" + variantSuffix(ref.elem)
	case refAny:
		return "JSON"
	case refPrimitive:
	}

	return GoName(strings.TrimPrefix(ref.expr, "time."))
}

// assignDiscriminator records which property value selects each variant. An
// explicit discriminator wins; otherwise a property that every variant pins
// with const is used.
func (b *builder) assignDiscriminator(
	unionDecl *decl,
	info *schemaInfo,
	locations []asyncapi.Location,
) error {
	property := info.discriminatorProperty
	if property == "" {
		property = implicitDiscriminator(unionDecl.variants)
	}

	if property == "" {
		return nil
	}

	unionDecl.discriminatorJSON = property

	mapped, err := b.discriminatorMapping(info, locations)
	if err != nil {
		return err
	}

	for i, unionVariant := range unionDecl.variants {
		pinned := constOf(unionVariant, property)

		switch {
		case len(mapped[i]) > 0:
			unionVariant.discriminatorValues = mapped[i]
		case pinned != "":
			unionVariant.discriminatorValues = []string{pinned}
		case unionVariant.ref.kind == refNamed:
			unionVariant.discriminatorValues = []string{
				componentOrName(locations[i], unionVariant.ref.decl.name),
			}
		}
	}

	return nil
}

func (b *builder) discriminatorMapping(
	info *schemaInfo,
	locations []asyncapi.Location,
) (map[int][]string, error) {
	mapped := map[int][]string{}
	if info.discriminatorMapping == nil {
		return mapped, nil
	}

	for _, value := range info.discriminatorMapping.Keys {
		target, _ := info.discriminatorMapping.Values[value].(string)
		if !strings.Contains(target, "#") && !strings.Contains(target, "/") {
			// A bare schema name, as OpenAPI allows in discriminator mappings.
			target = "#" + componentSchemasPrefix + target
		}

		_, targetAt, err := b.resolver.Deref(&asyncapi.Object{
			Keys:   []string{"$ref"},
			Values: map[string]any{"$ref": target},
		}, locations[0])
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "discriminator mapping %q", value)
		}

		for i, variantAt := range locations {
			if variantAt == targetAt {
				mapped[i] = append(mapped[i], value)
			}
		}
	}

	return mapped, nil
}

func implicitDiscriminator(variants []*variant) string {
	if len(variants) == 0 || !isStructVariant(variants[0]) {
		return ""
	}

	for _, candidate := range variants[0].ref.decl.fields {
		if candidate.constJSON == "" {
			continue
		}

		if everyVariantPins(variants, candidate.jsonName) {
			return candidate.jsonName
		}
	}

	return ""
}

// isStructVariant reports whether a union variant is a generated struct.
func isStructVariant(unionVariant *variant) bool {
	return unionVariant.ref.kind == refNamed &&
		unionVariant.ref.decl.kind == declStruct
}

func everyVariantPins(variants []*variant, property string) bool {
	seen := map[string]bool{}

	for _, unionVariant := range variants {
		value := constOf(unionVariant, property)
		if value == "" || seen[value] {
			return false
		}

		seen[value] = true
	}

	return true
}

// constOf returns the raw const value a struct variant pins property to.
func constOf(unionVariant *variant, property string) string {
	if !isStructVariant(unionVariant) {
		return ""
	}

	for _, candidate := range unionVariant.ref.decl.fields {
		if candidate.jsonName != property || candidate.constJSON == "" {
			continue
		}

		var raw any
		if err := json.Unmarshal(
			[]byte(candidate.constJSON),
			&raw,
		); err != nil {
			return ""
		}

		text, ok := raw.(string)
		if !ok {
			return candidate.constJSON
		}

		return text
	}

	return ""
}

func componentOrName(location asyncapi.Location, fallback string) string {
	if name, ok := componentSchemaName(location); ok {
		return name
	}

	return fallback
}

func isComponentSchema(location asyncapi.Location) bool {
	_, ok := componentSchemaName(location)

	return ok
}

func componentSchemaName(location asyncapi.Location) (string, bool) {
	name, ok := strings.CutPrefix(location.Pointer, componentSchemasPrefix)
	if !ok || strings.Contains(name, "/") {
		return "", false
	}

	return location.LastToken(), true
}

// goLiteral returns value as Go source.
func goLiteral(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return strconv.Quote(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), true
	case bool:
		return strconv.FormatBool(typed), true
	default:
		return "", false
	}
}

// literalLabel names an enum constant after its value.
func literalLabel(value any) string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return "Empty"
		}

		return typed
	case int64:
		label := strconv.FormatInt(typed, 10)
		if typed < 0 {
			return "Minus" + strings.TrimPrefix(label, "-")
		}

		return label
	case float64:
		formatted := strconv.FormatFloat(typed, 'g', -1, 64)
		replacer := strings.NewReplacer("-", "Minus", ".", "Point")

		return replacer.Replace(formatted)
	default:
		return fallbackIdentifier
	}
}
