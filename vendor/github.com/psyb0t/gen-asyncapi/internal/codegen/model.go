package codegen

type declKind int

const (
	declStruct declKind = iota
	declEnum
	declUnion
	declAlias
)

type refKind int

const (
	refPrimitive refKind = iota
	refNamed
	refArray
	refMap
	refAny
)

// typeRef is how a schema is used at one place: the Go type expression plus
// the constraints the generated Validate method checks on the value.
type typeRef struct {
	kind  refKind
	expr  string
	elem  *typeRef
	decl  *decl
	check []check
	// jsonType is the JSON type of a primitive, used to pick literal syntax.
	jsonType string
	// isTime marks time.Time, which needs the "time" import.
	isTime bool
}

// needsValidation reports whether values of this type have anything to check.
func (t *typeRef) needsValidation() bool {
	if len(t.check) > 0 {
		return true
	}

	switch t.kind {
	case refNamed:
		return t.decl.kind != declAlias || t.decl.alias.needsValidation()
	case refArray, refMap:
		return t.elem.needsValidation()
	case refPrimitive, refAny:
	}

	return false
}

// isNillable reports whether the Go zero value of the type is nil, so an
// optional field can use it directly instead of a pointer.
func (t *typeRef) isNillable() bool {
	switch t.kind {
	case refArray, refMap, refAny:
		return true
	case refNamed:
		return t.decl.kind == declAlias && t.decl.alias.isNillable()
	case refPrimitive:
	}

	return false
}

type checkKind int

const (
	checkMinLength checkKind = iota
	checkMaxLength
	checkPattern
	checkMinimum
	checkMaximum
	checkExclusiveMinimum
	checkExclusiveMaximum
	checkMinItems
	checkMaxItems
	checkMultipleOf
	checkUniqueItems
	checkMinProperties
	checkMaxProperties
	checkKeyPattern
)

// check is one constraint. arg is a Go literal ready to paste into code.
type check struct {
	kind checkKind
	arg  string
}

// decl is one named Go type the generator emits.
type decl struct {
	kind declKind
	name string
	doc  string

	fields     []*field
	strictKeys bool
	// minProperties/maxProperties are Go literals, "" when unset.
	minProperties string
	maxProperties string
	dependents    []dependent

	enumBase   string
	enumValues []enumValue

	variants          []*variant
	discriminatorJSON string

	alias *typeRef
}

type field struct {
	goName   string
	jsonName string
	doc      string
	ref      *typeRef
	required bool
	pointer  bool
	omit     bool
	// nullable marks a field whose schema allows null.
	nullable bool

	// constLiteral is the Go literal of a const field, filled in on marshal and
	// checked on unmarshal. constJSON is the same value as JSON text.
	constLiteral string
	constJSON    string

	// content is set for a string field that carries JSON described by
	// contentSchema.
	content *typeRef
}

// dependent is a dependentRequired entry: when property is present, every
// name in requires must be too.
type dependent struct {
	property string
	requires []string
}

type enumValue struct {
	goName  string
	literal string
}

type variant struct {
	suffix string
	ref    *typeRef
	// discriminatorValues are the raw discriminator values that select this
	// variant.
	discriminatorValues []string
}
