package codegen

import (
	"fmt"
	"strconv"
	"strings"
)

const runtimeAlias = "asyncapirt"

type declData struct {
	Kind string
	Name string
	Doc  []string

	Fields        []fieldData
	RequiredJSON  []string
	KnownJSON     []string
	StrictKeys    bool
	MinProperties string
	MaxProperties string
	Dependents    []dependentData
	ConstFields   []fieldData
	ContentFields []fieldData
	ValidateLines []string

	EnumBase   string
	EnumValues []enumValueData

	Variants      []variantData
	Discriminator string

	AliasExpr string
}

type fieldData struct {
	GoName       string
	JSONName     string
	TypeExpr     string
	Tag          string
	Doc          []string
	ConstLiteral string
	IsPointer    bool
	ContentType  string
}

type dependentData struct {
	Property string
	Requires string
}

type enumValueData struct {
	GoName  string
	Literal string
}

type variantData struct {
	Suffix        string
	Type          string
	Values        []string
	ValidateLines []string
}

const (
	declKindStruct = "struct"
	declKindEnum   = "enum"
	declKindUnion  = "union"
	declKindAlias  = "alias"
)

func renderDecl(d *decl) declData {
	data := declData{Name: d.name, Doc: commentLines(d.doc)}

	switch d.kind {
	case declStruct:
		data.Kind = declKindStruct
		renderStruct(d, &data)
	case declEnum:
		data.Kind = declKindEnum

		data.EnumBase = d.enumBase
		for _, value := range d.enumValues {
			data.EnumValues = append(
				data.EnumValues,
				enumValueData{GoName: value.goName, Literal: value.literal},
			)
		}
	case declUnion:
		data.Kind = declKindUnion

		data.Discriminator = d.discriminatorJSON
		for _, unionVariant := range d.variants {
			quoted := make([]string, 0, len(unionVariant.discriminatorValues))
			for _, value := range unionVariant.discriminatorValues {
				quoted = append(quoted, strconv.Quote(value))
			}

			rendered := variantData{
				Suffix: unionVariant.suffix,
				Type:   unionVariant.ref.expr,
				Values: quoted,
			}

			// Named variants validate themselves while decoding; inline ones
			// (a string with a pattern, an array with maxItems) are checked
			// here.
			if unionVariant.ref.kind != refNamed {
				rendered.ValidateLines = validationLines(
					"decoded",
					unionVariant.ref,
					strconv.Quote(d.name),
					0,
				)
			}

			data.Variants = append(data.Variants, rendered)
		}
	case declAlias:
		data.Kind = declKindAlias
		data.AliasExpr = d.alias.expr
	}

	return data
}

func renderStruct(d *decl, data *declData) {
	data.StrictKeys = d.strictKeys
	data.MinProperties = d.minProperties
	data.MaxProperties = d.maxProperties

	for _, entry := range d.dependents {
		quoted := make([]string, 0, len(entry.requires))
		for _, name := range entry.requires {
			quoted = append(quoted, strconv.Quote(name))
		}

		data.Dependents = append(data.Dependents, dependentData{
			Property: strconv.Quote(entry.property),
			Requires: strings.Join(quoted, ", "),
		})
	}

	for _, structField := range d.fields {
		rendered := renderField(structField)

		if structField.content != nil {
			data.ContentFields = append(data.ContentFields, rendered)
		}

		data.Fields = append(data.Fields, rendered)
		data.KnownJSON = append(
			data.KnownJSON,
			strconv.Quote(structField.jsonName),
		)

		if structField.required {
			data.RequiredJSON = append(
				data.RequiredJSON,
				strconv.Quote(structField.jsonName),
			)
		}

		if structField.constLiteral != "" {
			data.ConstFields = append(data.ConstFields, rendered)

			continue
		}

		data.ValidateLines = append(
			data.ValidateLines,
			fieldValidation(structField)...,
		)
	}
}

func renderField(structField *field) fieldData {
	typeExpr := structField.ref.expr
	if structField.pointer {
		typeExpr = "*" + typeExpr
	}

	tag := structField.jsonName
	if structField.omit && structField.constLiteral == "" {
		tag += ",omitempty"
	}

	rendered := fieldData{
		GoName:       structField.goName,
		JSONName:     structField.jsonName,
		TypeExpr:     typeExpr,
		Tag:          "`json:" + strconv.Quote(tag) + "`",
		Doc:          commentLines(structField.doc),
		ConstLiteral: structField.constLiteral,
		IsPointer:    structField.pointer,
	}

	if structField.content != nil {
		rendered.ContentType = structField.content.expr
	}

	return rendered
}

func fieldValidation(structField *field) []string {
	if !structField.ref.needsValidation() {
		return nil
	}

	name := strconv.Quote(structField.jsonName)
	target := "v." + structField.goName

	if !structField.pointer {
		return validationLines(target, structField.ref, name, 0)
	}

	lines := []string{"if " + target + " != nil {"}
	lines = append(
		lines,
		validationLines("(*"+target+")", structField.ref, name, 0)...,
	)

	return append(lines, "}")
}

// validationLines returns Go statements that check target, a value of type ref,
// and return a validation error naming it with the Go string expression name.
func validationLines(
	target string,
	ref *typeRef,
	name string,
	depth int,
) []string {
	lines := make([]string, 0, len(ref.check))
	for _, constraint := range ref.check {
		lines = append(lines, checkLine(target, name, constraint))
	}

	switch ref.kind {
	case refNamed:
		if ref.decl.kind == declAlias {
			return append(
				lines,
				validationLines(target, ref.decl.alias, name, depth)...,
			)
		}

		lines = append(lines,
			"if err := "+target+".Validate(); err != nil {",
			"return ctxerrors.Wrap(err, "+name+")",
			"}",
		)
	case refArray:
		lines = append(lines, arrayValidation(target, ref, name, depth)...)
	case refMap:
		lines = append(lines, mapValidation(target, ref, name, depth)...)
	case refPrimitive, refAny:
	}

	return lines
}

// arrayValidation returns the loop that validates every item of an array.
func arrayValidation(
	target string,
	ref *typeRef,
	name string,
	depth int,
) []string {
	if !ref.elem.needsValidation() {
		return nil
	}

	index := "i" + strconv.Itoa(depth)
	item := "item" + strconv.Itoa(depth)
	itemName := fmt.Sprintf(
		"fmt.Sprintf(\"%%s[%%d]\", %s, %s)",
		name,
		index,
	)

	header := "for " + index + ", " + item + " := range " + target + " {"
	lines := []string{header}
	lines = append(
		lines,
		validationLines(item, ref.elem, itemName, depth+1)...,
	)

	return append(lines, "}")
}

// mapValidation returns the loop that validates every value of a map.
func mapValidation(
	target string,
	ref *typeRef,
	name string,
	depth int,
) []string {
	if !ref.elem.needsValidation() {
		return nil
	}

	key := "key" + strconv.Itoa(depth)
	value := "value" + strconv.Itoa(depth)
	valueName := name + " + \".\" + " + key

	header := "for " + key + ", " + value + " := range " + target + " {"
	lines := []string{header}
	lines = append(
		lines,
		validationLines(value, ref.elem, valueName, depth+1)...,
	)

	return append(lines, "}")
}

// keyPatternLine is the statement that checks every map key against a
// pattern.
//
//nolint:lll,dupword // one generated Go statement, the key is passed twice
const keyPatternLine = `for key := range %s { if err := %s.CheckPattern(%s + "." + key, key, %s); err != nil { return err } }`

const conversionLen = "len"

// checkCall describes the runtime function a constraint calls. The checked
// value is passed through conversion, for example string, float64 or len, and
// takesLimit says whether the constraint's argument follows it.
type checkCall struct {
	function   string
	conversion string
	takesLimit bool
}

// checkCalls maps every constraint except key patterns to its runtime call.
func checkCalls() map[checkKind]checkCall {
	return map[checkKind]checkCall{
		checkMinLength: {"CheckMinLength", goTypeString, true},
		checkMaxLength: {"CheckMaxLength", goTypeString, true},
		checkPattern:   {"CheckPattern", goTypeString, true},
		checkMinimum:   {"CheckMinimum", goTypeFloat64, true},
		checkMaximum:   {"CheckMaximum", goTypeFloat64, true},
		checkExclusiveMinimum: {
			"CheckExclusiveMinimum", goTypeFloat64, true,
		},
		checkExclusiveMaximum: {
			"CheckExclusiveMaximum", goTypeFloat64, true,
		},
		checkMinItems:      {"CheckMinItems", conversionLen, true},
		checkMaxItems:      {"CheckMaxItems", conversionLen, true},
		checkMultipleOf:    {"CheckMultipleOf", goTypeFloat64, true},
		checkUniqueItems:   {"CheckUniqueItems", "", false},
		checkMinProperties: {"CheckMinProperties", conversionLen, true},
		checkMaxProperties: {"CheckMaxProperties", conversionLen, true},
	}
}

func checkLine(target, name string, constraint check) string {
	if constraint.kind == checkKeyPattern {
		return fmt.Sprintf(
			keyPatternLine,
			target,
			runtimeAlias,
			name,
			constraint.arg,
		)
	}

	call := checkCalls()[constraint.kind]

	subject := target
	if call.conversion != "" {
		subject = call.conversion + "(" + target + ")"
	}

	arguments := []string{name, subject}
	if call.takesLimit {
		arguments = append(arguments, constraint.arg)
	}

	return "if err := " + runtimeAlias + "." + call.function + "(" +
		strings.Join(arguments, ", ") + "); err != nil { return err }"
}

// commentLines splits a description into lines ready to prefix with "// ".
func commentLines(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	return strings.Split(text, "\n")
}
