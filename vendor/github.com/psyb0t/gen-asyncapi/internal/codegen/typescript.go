package codegen

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/psyb0t/ctxerrors"
)

const (
	typeScriptTemplateName = "typescript.tmpl"
	typeScriptRuntimeFile  = "templates/runtime.ts"
	// runtimeMarker starts each helper block in the runtime file.
	runtimeMarker  = "//@rt "
	tsIndentUnit   = "  "
	tsHandlePrefix = "handle"

	// Indent levels of generated statements inside the template: a client
	// method body, and a check function inside the received-messages table.
	tsMethodIndent       = 2
	tsCandidateIndent    = 3
	tsFunctionBodyIndent = 1
)

// tsRuntimeReference finds the runtime helpers a piece of TypeScript calls.
var tsRuntimeReference = regexp.MustCompile(`\brt[A-Z][A-Za-z0-9]*\b`)

// tsIdentifier matches a property name TypeScript accepts without quotes.
var tsIdentifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

type tsFileData struct {
	Title       string
	SpecVersion string
	Servers     []tsServerData
	Channels    []tsChannelData
	Decls       []tsDeclData
	Messages    []tsMessageData
	Received    []tsMessageData
	Sends       []tsSendData
	Runtime     string
}

type tsServerData struct {
	Name     string
	Function string
	URL      string
	Template string
	Vars     []tsParamData
	Doc      []string
}

type tsChannelData struct {
	Name     string
	Function string
	Address  string
	Params   []tsParamData
	Doc      []string
}

type tsParamData struct {
	Placeholder string
	ArgName     string
}

type tsDeclData struct {
	Kind      string
	Name      string
	Validator string
	Doc       []string

	Fields     []tsFieldData
	EnumValues []string
	Variants   []string
	AliasExpr  string
	Body       []string
	Contents   []tsContentData
}

type tsFieldData struct {
	Property string
	Type     string
	Optional bool
	Doc      []string
}

type tsContentData struct {
	Getter   string
	Setter   string
	Property string
	Path     string
	Type     string
	// Conditions is the test for an unset property, empty when the type
	// rules that out.
	Conditions string
	Body       []string
}

type tsMessageData struct {
	ConstName           string
	Name                string
	Doc                 []string
	CorrelationLocation string
	PayloadType         string
	Handler             string
	Matchers            []tsMatcherData
	Fallback            bool
	Body                []string
}

type tsMatcherData struct {
	Pointer string
	Value   string
}

type tsSendData struct {
	Method      string
	PayloadType string
	Message     string
	Doc         []string
	Body        []string
}

// renderTypeScript renders the TypeScript file for data and the schema
// declarations the builder made.
func renderTypeScript(data fileData, decls []*decl) ([]byte, error) {
	file := tsFileData{
		Title:       data.Title,
		SpecVersion: data.SpecVersion,
		Servers:     tsServers(data.Servers),
		Channels:    tsChannels(data.Channels),
		Messages:    tsMessages(data.Messages),
		Received:    tsMessages(data.Received),
		Sends:       tsSends(data.Sends),
	}

	for _, d := range decls {
		file.Decls = append(file.Decls, tsDecl(d))
	}

	tmpl, err := template.New(typeScriptTemplateName).Funcs(template.FuncMap{
		"doc":  tsDoc,
		"join": strings.Join,
	}).ParseFS(templateFiles, "templates/"+typeScriptTemplateName)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "parse TypeScript template")
	}

	body, err := executeTemplate(tmpl, file)
	if err != nil {
		return nil, err
	}

	file.Runtime, err = tsRuntime(body)
	if err != nil {
		return nil, err
	}

	return executeTemplate(tmpl, file)
}

func executeTemplate(tmpl *template.Template, data any) ([]byte, error) {
	var source bytes.Buffer
	if err := tmpl.Execute(&source, data); err != nil {
		return nil, ctxerrors.Wrap(err, "execute template")
	}

	return source.Bytes(), nil
}

// tsRuntime returns the runtime helpers source calls, with the helpers those
// call in turn, in the order the runtime file defines them.
func tsRuntime(source []byte) (string, error) {
	runtimeSource, err := templateFiles.ReadFile(typeScriptRuntimeFile)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read TypeScript runtime")
	}

	blocks, order := tsRuntimeBlocks(string(runtimeSource))

	needed := map[string]bool{}
	pending := tsRuntimeReference.FindAllString(string(source), -1)

	for len(pending) > 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]

		block, isBlock := blocks[name]
		if !isBlock || needed[name] {
			continue
		}

		needed[name] = true

		pending = append(
			pending,
			tsRuntimeReference.FindAllString(block, -1)...,
		)
	}

	parts := make([]string, 0, len(needed))
	for _, name := range order {
		if needed[name] {
			parts = append(parts, blocks[name])
		}
	}

	return strings.Join(parts, "\n\n"), nil
}

// tsRuntimeBlocks splits the runtime file into its marked helper blocks.
func tsRuntimeBlocks(source string) (map[string]string, []string) {
	blocks := map[string]string{}

	var order []string

	for chunk := range strings.SplitSeq(source, runtimeMarker) {
		name, block, found := strings.Cut(chunk, "\n")
		if !found || name == "" {
			continue
		}

		blocks[name] = strings.TrimSpace(block)
		order = append(order, name)
	}

	return blocks, order
}

func tsServers(servers []serverData) []tsServerData {
	result := make([]tsServerData, 0, len(servers))

	for _, server := range servers {
		result = append(result, tsServerData{
			Name:     server.GoName,
			Function: lowerFirst(server.GoName) + "URLWith",
			URL:      tsFromGoString(server.URL),
			Template: tsFromGoString(server.Template),
			Vars:     tsParams(server.Vars),
			Doc:      server.Doc,
		})
	}

	return result
}

func tsChannels(channels []channelData) []tsChannelData {
	result := make([]tsChannelData, 0, len(channels))

	for _, channel := range channels {
		if channel.Address == "" {
			continue
		}

		result = append(result, tsChannelData{
			Name:     channel.GoName,
			Function: lowerFirst(channel.GoName) + "Address",
			Address:  tsFromGoString(channel.Address),
			Params:   tsParams(channel.Params),
			Doc:      channel.Doc,
		})
	}

	return result
}

func tsParams(params []paramData) []tsParamData {
	result := make([]tsParamData, 0, len(params))

	for _, param := range params {
		result = append(result, tsParamData{
			Placeholder: tsFromGoString(param.Name),
			ArgName:     tsParamName(param.ArgName),
		})
	}

	return result
}

func tsDecl(d *decl) tsDeclData {
	data := tsDeclData{
		Name:      d.name,
		Validator: tsValidatorName(d.name),
		Doc:       commentLines(d.doc),
	}

	switch d.kind {
	case declStruct:
		data.Kind = declKindStruct
		tsStruct(d, &data)
	case declEnum:
		data.Kind = declKindEnum
		tsEnum(d, &data)
	case declUnion:
		data.Kind = declKindUnion
		tsUnion(d, &data)
	case declAlias:
		data.Kind = declKindAlias
		data.AliasExpr = tsType(d.alias)
		data.Body = (&tsChecker{}).lines("value", "path", d.alias)
	}

	if len(data.Body) == 0 {
		data.Body = []string{tsCall("rtUnknown", "value", "path")}
	}

	data.Body = tsIndent(data.Body, tsFunctionBodyIndent)

	return data
}

func tsEnum(d *decl, data *tsDeclData) {
	data.AliasExpr = tsTypeNumber
	if d.enumBase == goTypeString {
		data.AliasExpr = tsTypeString
	}

	for _, value := range d.enumValues {
		data.EnumValues = append(
			data.EnumValues,
			tsFromGoLiteral(value.literal),
		)
	}

	data.Body = []string{
		tsCall("rtEnum", "path", "value", d.name+"Values"),
	}
}

func tsStruct(d *decl, data *tsDeclData) {
	for _, structField := range d.fields {
		data.Fields = append(data.Fields, tsField(structField))

		if structField.content != nil {
			data.Contents = append(data.Contents, tsContent(d, structField))
		}
	}

	body := tsObjectChecks(d)
	checker := &tsChecker{}

	for _, structField := range d.fields {
		body = append(body, tsFieldChecks(checker, structField)...)
	}

	usesObject := slices.ContainsFunc(body, func(line string) bool {
		return strings.Contains(line, "object[") ||
			strings.Contains(line, "(object)") ||
			strings.Contains(line, ", object, ")
	})

	declaration := "const object = rtObject(value, path);"
	if !usesObject {
		declaration = tsCall("rtObject", "value", "path")
	}

	data.Body = append([]string{declaration}, body...)
}

// tsObjectChecks returns the object-level checks of a struct: required and
// unknown properties, property counts and dependentRequired.
func tsObjectChecks(d *decl) []string {
	var (
		lines    []string
		required []string
		known    = make([]string, 0, len(d.fields))
		count    = "Object.keys(object).length"
	)

	for _, structField := range d.fields {
		known = append(known, tsQuote(structField.jsonName))
		if structField.required {
			required = append(required, tsQuote(structField.jsonName))
		}
	}

	if len(required) > 0 {
		lines = append(
			lines,
			tsCall("rtRequire", "path", "object", tsArray(required)),
		)
	}

	if d.strictKeys {
		lines = append(
			lines,
			tsCall("rtKnownKeys", "path", "object", tsArray(known)),
		)
	}

	if d.minProperties != "" {
		lines = append(
			lines,
			tsCall("rtMinProperties", "path", count, d.minProperties),
		)
	}

	if d.maxProperties != "" {
		lines = append(
			lines,
			tsCall("rtMaxProperties", "path", count, d.maxProperties),
		)
	}

	for _, entry := range d.dependents {
		requires := make([]string, 0, len(entry.requires))
		for _, name := range entry.requires {
			requires = append(requires, tsQuote(name))
		}

		lines = append(lines, tsCall(
			"rtDependent",
			"path",
			"object",
			tsQuote(entry.property),
			tsArray(requires),
		))
	}

	return lines
}

// tsFieldChecks returns the checks of one struct property. Absent optional
// properties and null nullable ones are skipped.
func tsFieldChecks(checker *tsChecker, structField *field) []string {
	access := "object[" + tsQuote(structField.jsonName) + "]"
	path := "path + " + tsQuote("."+structField.jsonName)

	checks := checker.lines(access, path, structField.ref)
	if structField.constJSON != "" {
		checks = []string{
			tsCall("rtConst", path, access, structField.constJSON),
		}
	}

	if len(checks) == 0 {
		return nil
	}

	var conditions []string
	if !structField.required {
		conditions = append(conditions, access+" !== undefined")
	}

	if structField.nullable {
		conditions = append(conditions, access+" !== null")
	}

	if len(conditions) == 0 {
		return checks
	}

	lines := []string{"if (" + strings.Join(conditions, " && ") + ") {"}
	lines = append(lines, checks...)

	return append(lines, "}")
}

func tsField(structField *field) tsFieldData {
	fieldType := tsType(structField.ref)
	if structField.constJSON != "" {
		fieldType = structField.constJSON
	}

	if structField.nullable {
		fieldType += " | null"
	}

	return tsFieldData{
		Property: tsPropertyName(structField.jsonName),
		Type:     fieldType,
		Optional: !structField.required,
		Doc:      commentLines(structField.doc),
	}
}

func tsContent(d *decl, structField *field) tsContentData {
	path := tsQuote(d.name + "." + structField.jsonName)
	parsed := (&tsChecker{}).lines("parsed", path, structField.content)
	name := lowerFirst(d.name) + structField.goName + "Value"

	var conditions []string
	if !structField.required {
		conditions = append(conditions, "raw === undefined")
	}

	if structField.nullable {
		conditions = append(conditions, "raw === null")
	}

	return tsContentData{
		Getter:     name,
		Setter:     "set" + d.name + structField.goName + "Value",
		Property:   tsQuote(structField.jsonName),
		Path:       path,
		Type:       tsType(structField.content),
		Conditions: strings.Join(conditions, " || "),
		Body:       tsIndent(parsed, tsFunctionBodyIndent),
	}
}

func tsUnion(d *decl, data *tsDeclData) {
	for _, unionVariant := range d.variants {
		data.Variants = append(data.Variants, tsType(unionVariant.ref))
	}

	if d.discriminatorJSON == "" {
		data.Body = tsOneOf(d)

		return
	}

	data.Body = []string{
		"const object = rtObject(value, path);",
		"const discriminator = rtDiscriminator(path, object, " +
			tsQuote(d.discriminatorJSON) + ");",
		"switch (discriminator) {",
	}

	for _, unionVariant := range d.variants {
		values := unionVariant.discriminatorValues
		if len(values) == 0 {
			continue
		}

		for _, value := range values[:len(values)-1] {
			data.Body = append(data.Body, "case "+tsQuote(value)+":")
		}

		data.Body = append(
			data.Body,
			"case "+tsQuote(values[len(values)-1])+": {",
		)
		data.Body = append(
			data.Body,
			(&tsChecker{}).lines("value", "path", unionVariant.ref)...,
		)
		data.Body = append(data.Body, "return;", "}")
	}

	reason := tsQuote("unknown " + strconv.Quote(d.discriminatorJSON) + " ")

	data.Body = append(
		data.Body,
		"}",
		"rtFail(path, "+reason+" + JSON.stringify(discriminator));",
	)
}

func tsOneOf(d *decl) []string {
	lines := []string{"rtOneOf(path, ["}

	for _, unionVariant := range d.variants {
		checks := (&tsChecker{}).lines("value", "path", unionVariant.ref)
		lines = append(lines, "(): void => {")
		lines = append(lines, checks...)
		lines = append(lines, "},")
	}

	return append(lines, "]);")
}

func tsMessages(messages []messageData) []tsMessageData {
	result := make([]tsMessageData, 0, len(messages))

	for _, message := range messages {
		checks := (&tsChecker{}).lines(
			"payload",
			message.ConstName,
			message.payload,
		)

		data := tsMessageData{
			ConstName:   message.ConstName,
			Name:        tsQuote(message.Name),
			Doc:         tsMessageDoc(message),
			PayloadType: tsType(message.payload),
			Handler:     tsHandlePrefix + message.GoName,
			Fallback:    message.Fallback,
			Body:        tsIndent(checks, tsCandidateIndent),
		}

		if message.CorrelationLocation != "" {
			data.CorrelationLocation = tsFromGoString(
				message.CorrelationLocation,
			)
		}

		for _, matcher := range message.Matchers {
			data.Matchers = append(data.Matchers, tsMatcherData{
				Pointer: tsQuote(matcher.pointer),
				Value:   matcher.value,
			})
		}

		result = append(result, data)
	}

	return result
}

func tsMessageDoc(message messageData) []string {
	headersType := ""
	if message.headers != nil {
		headersType = tsType(message.headers)
	}

	return commentLines(
		messageDoc(message.message, tsType(message.payload), headersType),
	)
}

func tsSends(sends []sendData) []tsSendData {
	result := make([]tsSendData, 0, len(sends))

	for _, send := range sends {
		method := lowerFirst(send.Method)
		summary := method + " checks payload and sends it as a " +
			send.Message + " message."

		result = append(result, tsSendData{
			Method:      method,
			PayloadType: tsType(send.payload),
			Message:     send.Message,
			Doc:         append([]string{summary}, send.Doc...),
			Body: tsIndent(
				(&tsChecker{}).lines("payload", send.Message, send.payload),
				tsMethodIndent,
			),
		})
	}

	return result
}

// tsIndent indents generated statements by their bracket depth, starting at
// level. A line moves the depth by at most one level, and brackets inside
// string literals do not count.
func tsIndent(lines []string, level int) []string {
	result := make([]string, 0, len(lines))
	depth := level

	for _, line := range lines {
		opens, leadingCloses, closes := tsBraces(line)

		lineDepth := depth
		if leadingCloses > 0 {
			lineDepth--
		}

		result = append(
			result,
			strings.Repeat(tsIndentUnit, max(lineDepth, 0))+line,
		)

		switch {
		case opens > closes:
			depth++
		case opens < closes:
			depth--
		}
	}

	return result
}

// tsBraces counts the opening and closing brackets of a line outside string
// literals, and the closing ones it starts with.
func tsBraces(line string) (int, int, int) {
	var (
		opens, closes, leadingCloses int
		inString, escaped, leading   = false, false, true
	)

	for _, character := range line {
		switch {
		case escaped:
			escaped = false
		case inString && character == '\\':
			escaped = true
		case character == '"':
			inString = !inString
		case inString:
		case strings.ContainsRune("{([", character):
			opens++
		case strings.ContainsRune("})]", character):
			closes++

			if leading {
				leadingCloses++
			}

			continue
		}

		leading = false
	}

	return opens, leadingCloses, closes
}

// tsDoc renders lines as a JSDoc comment indented by indent, or nothing.
func tsDoc(lines []string, indent string) string {
	if len(lines) == 0 {
		return ""
	}

	escaper := strings.NewReplacer("*/", `*\/`)
	rendered := []string{indent + "/**"}

	for _, line := range lines {
		rendered = append(
			rendered,
			strings.TrimRight(indent+" * "+escaper.Replace(line), " "),
		)
	}

	return strings.Join(append(rendered, indent+" */"), "\n") + "\n"
}

// tsQuote returns text as a TypeScript string literal.
func tsQuote(text string) string {
	var encoded bytes.Buffer

	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(text); err != nil {
		return strconv.Quote(text)
	}

	return strings.TrimSuffix(encoded.String(), "\n")
}

// tsFromGoString turns a quoted Go string literal into a TypeScript one.
func tsFromGoString(literal string) string {
	text, err := strconv.Unquote(literal)
	if err != nil {
		return literal
	}

	return tsQuote(text)
}

// tsFromGoLiteral turns a Go scalar literal into a TypeScript one.
func tsFromGoLiteral(literal string) string {
	if strings.HasPrefix(literal, `"`) {
		return tsFromGoString(literal)
	}

	return literal
}

func tsArray(items []string) string {
	return "[" + strings.Join(items, ", ") + "]"
}

func tsPropertyName(name string) string {
	if tsIdentifier.MatchString(name) {
		return name
	}

	return tsQuote(name)
}

// tsReservedWords are the words a TypeScript parameter cannot be named.
func tsReservedWords() []string {
	return []string{
		"arguments", "await", "break", "case", "catch", "class", "const",
		"continue", "debugger", "default", "delete", "do", "else", "enum",
		"eval", "export", "extends", "false", "finally", "for", "function",
		"if", "implements", "import", "in", "instanceof", "interface", "let",
		"new", "null", "package", "private", "protected", "public", "return",
		"static", "super", "switch", "this", "throw", "true", "try", "typeof",
		"var", "void", "while", "with", "yield",
	}
}

func tsParamName(name string) string {
	if slices.Contains(tsReservedWords(), name) {
		return name + fallbackIdentifier
	}

	return name
}
