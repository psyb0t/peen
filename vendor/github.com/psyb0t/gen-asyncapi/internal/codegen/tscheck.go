package codegen

import (
	"strconv"
	"strings"
)

const (
	tsTypeString  = "string"
	tsTypeNumber  = "number"
	tsTypeBoolean = "boolean"
	tsTypeUnknown = "unknown"
	tsTypeAnyMap  = "Record<string, unknown>"
)

// tsType returns the TypeScript type expression for ref.
func tsType(ref *typeRef) string {
	switch ref.kind {
	case refNamed:
		return ref.decl.name
	case refArray:
		return "Array<" + tsType(ref.elem) + ">"
	case refMap:
		return "Record<string, " + tsType(ref.elem) + ">"
	case refAny:
		if ref.expr == goTypeAnyMap {
			return tsTypeAnyMap
		}

		return tsTypeUnknown
	case refPrimitive:
	}

	return tsPrimitiveType(ref)
}

func tsPrimitiveType(ref *typeRef) string {
	switch ref.jsonType {
	case jsonTypeString:
		return tsTypeString
	case jsonTypeInteger, jsonTypeNumber:
		return tsTypeNumber
	case jsonTypeBoolean:
		return tsTypeBoolean
	default:
		return tsTypeUnknown
	}
}

// tsChecker writes TypeScript statements that check an unknown value against
// a type and throw a ValidationError when it does not fit. Each statement
// list gets its own checker so local names stay unique within it.
type tsChecker struct {
	next int
}

func (c *tsChecker) local(prefix string) string {
	c.next++

	return prefix + strconv.Itoa(c.next)
}

// lines returns statements that check target, a TypeScript expression, as a
// value of ref. path is a TypeScript string expression naming the value in
// errors. No statements means anything fits.
func (c *tsChecker) lines(target, path string, ref *typeRef) []string {
	switch ref.kind {
	case refNamed:
		return []string{tsCall(tsValidatorName(ref.decl.name), target, path)}
	case refPrimitive:
		return c.primitive(target, path, ref)
	case refArray:
		return c.array(target, path, ref)
	case refMap:
		return c.mapLines(target, path, ref)
	case refAny:
		if ref.expr != goTypeAnyMap {
			return nil
		}

		return c.bound(target, path, "rtObject", ref.check, "o", nil)
	}

	return nil
}

func (c *tsChecker) primitive(target, path string, ref *typeRef) []string {
	return c.bound(target, path, tsPrimitiveCheck(ref), ref.check, "v", nil)
}

// bound calls the type check function on target, then runs checks and the
// statements more returns against its result, bound to a local when needed.
func (c *tsChecker) bound(
	target, path, function string,
	checks []check,
	prefix string,
	more func(local string) []string,
) []string {
	local := c.local(prefix)

	var body []string
	for _, constraint := range checks {
		body = append(body, tsCheckLine(path, local, constraint))
	}

	if more != nil {
		body = append(body, more(local)...)
	}

	if len(body) == 0 {
		return []string{tsCall(function, target, path)}
	}

	declaration := "const " + local + " = " + function +
		"(" + target + ", " + path + ");"

	return append([]string{declaration}, body...)
}

func (c *tsChecker) array(target, path string, ref *typeRef) []string {
	return c.bound(
		target,
		path,
		"rtArray",
		ref.check,
		"a",
		func(local string) []string {
			index := c.local("i")
			item := c.lines(
				local+"["+index+"]",
				path+` + "[" + `+index+` + "]"`,
				ref.elem,
			)

			if len(item) == 0 {
				return nil
			}

			header := "for (let " + index + " = 0; " + index + " < " +
				local + ".length; " + index + "++) {"
			lines := append([]string{header}, item...)

			return append(lines, "}")
		},
	)
}

func (c *tsChecker) mapLines(target, path string, ref *typeRef) []string {
	return c.bound(
		target,
		path,
		"rtObject",
		ref.check,
		"m",
		func(local string) []string {
			key := c.local("k")
			entry := c.local("e")
			value := c.lines(entry, path+` + "." + `+key, ref.elem)

			if len(value) == 0 {
				return nil
			}

			header := "for (const [" + key + ", " + entry +
				"] of Object.entries(" + local + ")) {"
			lines := append([]string{header}, value...)

			return append(lines, "}")
		},
	)
}

// tsPrimitiveCheck names the runtime function that checks a primitive's type.
func tsPrimitiveCheck(ref *typeRef) string {
	switch {
	case ref.isTime:
		return "rtDateTime"
	case ref.expr == goTypeInt32:
		return "rtInt32"
	}

	switch ref.jsonType {
	case jsonTypeString:
		return "rtString"
	case jsonTypeInteger:
		return "rtInteger"
	case jsonTypeNumber:
		return "rtNumber"
	case jsonTypeBoolean:
		return "rtBoolean"
	default:
		return "rtUnknown"
	}
}

// tsSubject is how a constraint's runtime function receives the checked
// value.
type tsSubject int

const (
	tsSubjectValue tsSubject = iota
	tsSubjectLength
	tsSubjectKeyCount
)

type tsCheckCall struct {
	function string
	subject  tsSubject
}

// tsCheckCalls maps each constraint to its runtime function.
func tsCheckCalls() map[checkKind]tsCheckCall {
	return map[checkKind]tsCheckCall{
		checkMinLength:        {"rtMinLength", tsSubjectValue},
		checkMaxLength:        {"rtMaxLength", tsSubjectValue},
		checkPattern:          {"rtPattern", tsSubjectValue},
		checkMinimum:          {"rtMinimum", tsSubjectValue},
		checkMaximum:          {"rtMaximum", tsSubjectValue},
		checkExclusiveMinimum: {"rtExclusiveMinimum", tsSubjectValue},
		checkExclusiveMaximum: {"rtExclusiveMaximum", tsSubjectValue},
		checkMultipleOf:       {"rtMultipleOf", tsSubjectValue},
		checkMinItems:         {"rtMinItems", tsSubjectLength},
		checkMaxItems:         {"rtMaxItems", tsSubjectLength},
		checkUniqueItems:      {"rtUniqueItems", tsSubjectValue},
		checkMinProperties:    {"rtMinProperties", tsSubjectKeyCount},
		checkMaxProperties:    {"rtMaxProperties", tsSubjectKeyCount},
		checkKeyPattern:       {"rtKeyPattern", tsSubjectValue},
	}
}

func tsCheckLine(path, local string, constraint check) string {
	call := tsCheckCalls()[constraint.kind]

	subject := local

	switch call.subject {
	case tsSubjectLength:
		subject = local + ".length"
	case tsSubjectKeyCount:
		subject = "Object.keys(" + local + ").length"
	case tsSubjectValue:
	}

	arguments := []string{path, subject}

	isPattern := constraint.kind == checkPattern ||
		constraint.kind == checkKeyPattern

	switch {
	case constraint.kind == checkUniqueItems:
	case isPattern:
		arguments = append(arguments, tsFromGoString(constraint.arg))
	default:
		arguments = append(arguments, constraint.arg)
	}

	return tsCall(call.function, arguments...)
}

func tsCall(function string, arguments ...string) string {
	return function + "(" + strings.Join(arguments, ", ") + ");"
}

func tsValidatorName(typeName string) string {
	return "validate" + typeName
}
