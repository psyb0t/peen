package codegen

import (
	"bytes"
	"embed"
	"encoding/json"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	asyncapi "github.com/psyb0t/go-asyncapi"
	"golang.org/x/tools/imports"
)

//go:embed templates/*
var templateFiles embed.FS

const (
	templateName  = "file.tmpl"
	messagePrefix = "Message"
	serverPrefix  = "Server"
	payloadSuffix = "Payload"
)

// reservedNames are identifiers the generated code uses itself, so schema
// types never take them.
func reservedNames(language Language) []string {
	if language == LanguageTypeScript {
		return []string{
			"Client",
			"Handler",
			"Transport",
			"ReceivedMessage",
			"ValidationError",
			"UnknownMessageError",
			"AmbiguousMessageError",
			"RtMatcher",
			"RtCandidate",
		}
	}

	return []string{
		"Client",
		"NewClient",
		"Handler",
		"UnimplementedHandler",
		"ReceivedMessage",
		"DecodeReceivedMessage",
		"Dispatch",
	}
}

type fileData struct {
	Package     string
	Title       string
	SpecVersion string
	Servers     []serverData
	Channels    []channelData
	Decls       []declData
	Messages    []messageData
	Received    []messageData
	Sends       []sendData
}

type serverData struct {
	GoName   string
	URL      string
	Template string
	Vars     []paramData
	Doc      []string
}

type channelData struct {
	GoName  string
	Address string
	Params  []paramData
	Doc     []string
}

type paramData struct {
	Name    string
	ArgName string
}

type messageData struct {
	GoName              string
	ConstName           string
	Name                string
	PayloadType         string
	Doc                 []string
	Matchers            []matcherData
	CorrelationLocation string
	// Fallback marks a message whose payload has no schema to check.
	Fallback bool

	message *asyncapi.Message
	payload *typeRef
	headers *typeRef
}

type matcherData struct {
	Pointer string
	Value   string

	// pointer is the unquoted JSON pointer and value the expected JSON text.
	pointer string
	value   string
}

type sendData struct {
	Method      string
	PayloadType string
	Message     string
	Doc         []string

	payload *typeRef
}

// Generate renders the Go or TypeScript source for the AsyncAPI document at
// specPath. config must have passed Validate.
func Generate(config *Config, specPath string) ([]byte, error) {
	document, err := asyncapi.Load(specPath)
	if err != nil {
		return nil, ctxerrors.Wrapf(err, "load spec %s", specPath)
	}

	b := newBuilder(document.Resolver, reservedNames(config.Language)...)

	messages, byLocation, err := buildMessages(b, document, config)
	if err != nil {
		return nil, err
	}

	for _, component := range document.ComponentSchemas {
		if _, err := b.typeOf(
			component.Value,
			component.At,
			GoName(component.Name),
		); err != nil {
			return nil, ctxerrors.Wrapf(
				err,
				"component schema %s",
				component.Name,
			)
		}
	}

	data := fileData{
		Package:     config.Package,
		Title:       document.Title,
		SpecVersion: document.Version,
		Servers:     buildServers(document),
		Channels:    buildChannels(document),
		Messages:    messages,
	}

	if config.Generate.Client {
		data.Received, data.Sends = buildOperations(
			document,
			byLocation,
			config.Language,
		)
	}

	if config.Language == LanguageTypeScript {
		return renderTypeScript(data, b.decls)
	}

	for _, d := range b.decls {
		data.Decls = append(data.Decls, renderDecl(d))
	}

	return render(data)
}

func buildMessages(
	b *builder,
	document *asyncapi.Document,
	config *Config,
) ([]messageData, map[asyncapi.Location]messageData, error) {
	names := newNameSet(reservedNames(config.Language)...)
	wireNames := newNameSet()
	byLocation := map[asyncapi.Location]messageData{}
	configured := map[string]bool{}

	result := make([]messageData, 0, len(document.Messages))
	for _, message := range document.Messages {
		data, err := buildMessage(b, message, config, names, wireNames)
		if err != nil {
			return nil, nil, err
		}

		if _, ok := config.OutputOptions.MessageMatchers[message.Key]; ok {
			configured[message.Key] = true
		}

		result = append(result, data)
		byLocation[message.Location] = data
	}

	for key := range config.OutputOptions.MessageMatchers {
		if !configured[key] {
			return nil, nil, ctxerrors.Wrapf(
				commerr.ErrNotFound,
				"message-matchers key %q is not a message in the spec",
				key,
			)
		}
	}

	return result, byLocation, nil
}

// buildMessage resolves the payload and headers types of one message and
// assembles its template data.
func buildMessage(
	b *builder,
	message *asyncapi.Message,
	config *Config,
	names *nameSet,
	wireNames *nameSet,
) (messageData, error) {
	goName := names.claim(GoName(message.Key))

	payload, err := messagePayload(b, message, goName)
	if err != nil {
		return messageData{}, err
	}

	headers, err := messageHeaders(b, message, goName)
	if err != nil {
		return messageData{}, err
	}

	headersType := ""
	if headers != nil {
		headersType = headers.expr
	}

	// Message names identify received messages, so two messages sharing a
	// spec name fall back to their unique keys.
	wireName := message.Name
	if wireName == "" || wireNames.used[wireName] {
		wireName = message.Key
	}

	wireName = wireNames.claim(wireName)

	matchers, err := messageMatchers(config, message.Key, payload)
	if err != nil {
		return messageData{}, err
	}

	data := messageData{
		GoName:      goName,
		ConstName:   messagePrefix + goName,
		Name:        wireName,
		PayloadType: payload.expr,
		Fallback:    payload.kind == refAny,
		Doc: commentLines(
			messageDoc(message, payload.expr, headersType),
		),
		Matchers: matchers,
		message:  message,
		payload:  payload,
		headers:  headers,
	}

	if message.CorrelationIDLocation != "" {
		data.CorrelationLocation = strconv.Quote(
			message.CorrelationIDLocation,
		)
	}

	return data, nil
}

// messagePayload returns the payload type of a message, or a raw JSON type
// when the message has no payload schema.
func messagePayload(
	b *builder,
	message *asyncapi.Message,
	goName string,
) (*typeRef, error) {
	if message.Payload == nil {
		return &typeRef{kind: refAny, expr: goTypeRawJSON}, nil
	}

	payload, err := b.typeOf(
		message.Payload,
		message.PayloadAt,
		goName+payloadSuffix,
	)
	if err != nil {
		return nil, ctxerrors.Wrapf(
			err,
			"message %s payload",
			message.Key,
		)
	}

	return payload, nil
}

// messageHeaders returns the type of a message's application headers, or nil
// when it declares none.
func messageHeaders(
	b *builder,
	message *asyncapi.Message,
	goName string,
) (*typeRef, error) {
	if message.Headers == nil {
		return nil, nil //nolint:nilnil // no headers is not an error
	}

	headers, err := b.typeOf(
		message.Headers,
		message.HeadersAt,
		goName+"Headers",
	)
	if err != nil {
		return nil, ctxerrors.Wrapf(
			err,
			"message %s headers",
			message.Key,
		)
	}

	return headers, nil
}

// messageMatchers combines the configured match rules with the consts the
// payload's top-level properties pin.
func messageMatchers(
	config *Config,
	key string,
	payload *typeRef,
) ([]matcherData, error) {
	var matchers []matcherData

	rules := config.OutputOptions.MessageMatchers[key]

	pointers := make([]string, 0, len(rules))
	for pointer := range rules {
		pointers = append(pointers, pointer)
	}

	slices.Sort(pointers)

	for _, pointer := range pointers {
		value, err := json.Marshal(rules[pointer])
		if err != nil {
			return nil, ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"message %s match %s: %v",
				key,
				pointer,
				err,
			)
		}

		matchers = append(matchers, matcherData{
			Pointer: strconv.Quote(pointer),
			Value:   strconv.Quote(string(value)),
			pointer: pointer,
			value:   string(value),
		})
	}

	if payload.kind != refNamed || payload.decl.kind != declStruct {
		return matchers, nil
	}

	for _, structField := range payload.decl.fields {
		if structField.constJSON == "" {
			continue
		}

		escaper := strings.NewReplacer("~", "~0", "/", "~1")
		pointer := "/" + escaper.Replace(structField.jsonName)
		matchers = append(matchers, matcherData{
			Pointer: strconv.Quote(pointer),
			Value:   strconv.Quote(structField.constJSON),
			pointer: pointer,
			value:   structField.constJSON,
		})
	}

	return matchers, nil
}

func buildServers(document *asyncapi.Document) []serverData {
	names := newNameSet()
	result := make([]serverData, 0, len(document.Servers))

	for _, server := range document.Servers {
		data := serverData{
			GoName:   serverPrefix + names.claim(GoName(server.Name)),
			URL:      strconv.Quote(server.URL()),
			Template: strconv.Quote(server.URLTemplate()),
			Doc:      commentLines(server.Description),
		}

		argNames := newNameSet("replacer")
		for _, variable := range server.Variables {
			data.Vars = append(data.Vars, paramData{
				Name:    strconv.Quote("{" + variable.Name + "}"),
				ArgName: argNames.claim(lowerFirst(GoName(variable.Name))),
			})
		}

		result = append(result, data)
	}

	return result
}

// messageDoc is the doc comment of a message constant.
func messageDoc(
	message *asyncapi.Message,
	payloadType, headersType string,
) string {
	lines := []string{
		firstNonEmpty(message.Summary, message.Description, message.Title),
		"Payload: " + payloadType + ".",
	}

	if headersType != "" {
		lines = append(lines, "Application headers: "+headersType+".")
	}

	if message.ContentType != "" {
		lines = append(lines, "Content type: "+message.ContentType+".")
	}

	if message.UnsupportedFormat != "" {
		lines = append(
			lines,
			"The payload schema format "+message.UnsupportedFormat+
				" is not JSON Schema, so the payload is raw JSON.",
		)
	}

	if message.Deprecated {
		lines = append(
			lines,
			"",
			"Deprecated: the spec marks this message as deprecated.",
		)
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func buildChannels(document *asyncapi.Document) []channelData {
	names := newNameSet()
	result := make([]channelData, 0, len(document.Channels))

	for _, channel := range document.Channels {
		data := channelData{
			GoName: names.claim(GoName(channel.Name)),
			Doc:    commentLines(channel.Description),
		}

		// A null address means the address is only known at runtime, such as
		// a reply channel named by the request; there is nothing to generate.
		if channel.Address != "" {
			data.Address = strconv.Quote(channel.Address)
		}

		argNames := newNameSet("address", "replacer")
		for _, parameter := range channel.Parameters {
			data.Params = append(data.Params, paramData{
				Name:    strconv.Quote("{" + parameter.Name + "}"),
				ArgName: argNames.claim(lowerFirst(GoName(parameter.Name))),
			})
		}

		result = append(result, data)
	}

	return result
}

// buildOperations returns the messages the client receives (from receive
// operations and from the replies to send operations) and one send method per
// message of each send operation.
func buildOperations(
	document *asyncapi.Document,
	byLocation map[asyncapi.Location]messageData,
	language Language,
) ([]messageData, []sendData) {
	reservedMethods := []string{"Run", "Close"}
	if language == LanguageTypeScript {
		reservedMethods = []string{"Close", "Receive", "Constructor"}
	}

	var (
		received []messageData
		sends    []sendData
		seen     = map[asyncapi.Location]bool{}
		methods  = newNameSet(reservedMethods...)
	)

	addReceived := func(messages []*asyncapi.Message) {
		for _, message := range messages {
			if seen[message.Location] {
				continue
			}

			seen[message.Location] = true
			received = append(received, byLocation[message.Location])
		}
	}

	for _, operation := range document.Operations {
		if operation.Action == asyncapi.ActionReceive {
			addReceived(operation.Messages)

			continue
		}

		if operation.Reply != nil {
			addReceived(operation.Reply.Messages)
		}

		for _, message := range operation.Messages {
			data := byLocation[message.Location]

			method := GoName(operation.Name)
			if len(operation.Messages) > 1 {
				method += data.GoName
			}

			sends = append(sends, sendData{
				Method:      methods.claim(method),
				PayloadType: data.PayloadType,
				Message:     data.ConstName,
				Doc: commentLines(
					firstNonEmpty(operation.Summary, operation.Description),
				),
				payload: data.payload,
			})
		}
	}

	return received, sends
}

func render(data fileData) ([]byte, error) {
	tmpl, err := template.New(templateName).Funcs(template.FuncMap{
		"join": strings.Join,
	}).ParseFS(templateFiles, "templates/"+templateName)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "parse templates")
	}

	var source bytes.Buffer
	if err := tmpl.ExecuteTemplate(&source, templateName, data); err != nil {
		return nil, ctxerrors.Wrap(err, "execute template")
	}

	formatted, err := imports.Process("", source.Bytes(), nil)
	if err != nil {
		return source.Bytes(), ctxerrors.Wrapf(
			commerr.ErrParseFailed,
			"format generated code: %v",
			err,
		)
	}

	return formatted, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}

// lowerFirst turns an exported identifier into an unexported one:
// "InstrumentID" becomes "instrumentID", "ID" becomes "id", and a result that
// is a Go keyword gets a "Value" suffix.
func lowerFirst(name string) string {
	if name == "" {
		return name
	}

	lowered := strings.ToLower(name)
	if strings.ToUpper(name) != name {
		runes := []rune(name)
		runes[0] = []rune(strings.ToLower(string(runes[0])))[0]
		lowered = string(runes)
	}

	if token.IsKeyword(lowered) {
		return lowered + fallbackIdentifier
	}

	return lowered
}
