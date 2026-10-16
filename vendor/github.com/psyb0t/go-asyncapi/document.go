// Package asyncapi loads AsyncAPI 3 documents. Load follows $refs across files,
// merges message and operation traits, and returns servers, channels,
// operations and messages in document order. Schemas stay as ordered
// Object trees, and the Document's Resolver dereferences the $refs inside
// them.
package asyncapi

import (
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const supportedMajorVersion = "3."

// Action is what an operation does: send or receive.
type Action string

// Operation actions.
const (
	// ActionSend marks an operation the application sends.
	ActionSend Action = "send"
	// ActionReceive marks an operation the application receives.
	ActionReceive Action = "receive"
)

// Document is a loaded AsyncAPI 3 document.
type Document struct {
	AsyncAPIVersion string
	Title           string
	Version         string
	Description     string
	// DefaultContentType applies to messages that set no contentType.
	DefaultContentType string
	Servers            []*Server
	Channels           []*Channel
	Operations         []*Operation
	// Messages lists every distinct message, in first-seen order.
	Messages []*Message
	// ComponentSchemas lists components.schemas in document order.
	ComponentSchemas []SchemaComponent

	// Resolver holds every loaded file and dereferences $refs inside schemas.
	Resolver *Resolver
	// Root is the location of the document's top level.
	Root Location

	channelsByLocation map[Location]*Channel
	messagesByLocation map[Location]*Message
}

// Server is one entry of the servers object.
type Server struct {
	Name            string
	Host            string
	Pathname        string
	Protocol        string
	ProtocolVersion string
	Description     string
	Variables       []*ServerVariable
}

// ServerVariable is a {name} placeholder in a server's host or pathname.
type ServerVariable struct {
	Name        string
	Default     string
	Enum        []string
	Description string
}

// URLTemplate returns protocol://host/pathname with variables unfilled.
func (s *Server) URLTemplate() string {
	return s.Protocol + "://" + s.Host + s.Pathname
}

// URL returns the server URL with every variable set to its default.
func (s *Server) URL() string {
	url := s.URLTemplate()
	for _, variable := range s.Variables {
		url = strings.ReplaceAll(url, "{"+variable.Name+"}", variable.Default)
	}

	return url
}

// Channel is one entry of the channels object.
type Channel struct {
	Name        string
	Address     string
	Description string
	Parameters  []*Parameter
	Messages    []*Message
	Location    Location
}

// Parameter is a channel address parameter.
type Parameter struct {
	Name        string
	Description string
	Enum        []string
	Default     string
}

// Message is a message definition. Messages referenced from several channels
// are the same *Message.
type Message struct {
	// Key is the components.messages key, or channel_message for an inline
	// message. Keys are unique within a document.
	Key         string
	Name        string
	Title       string
	Summary     string
	Description string
	// ContentType is the message's contentType, or the document's
	// defaultContentType.
	ContentType string
	Deprecated  bool
	// Payload is the payload schema as loaded, usually an *Object. It is nil
	// when the message has no payload or its schemaFormat is not AsyncAPI,
	// JSON Schema or OpenAPI; UnsupportedFormat names that format.
	Payload any
	// PayloadAt is where Payload sits, for resolving $refs inside it.
	PayloadAt         Location
	UnsupportedFormat string
	// Headers is the application headers schema, or nil.
	Headers   any
	HeadersAt Location
	// CorrelationIDLocation is the runtime expression of the correlationId,
	// such as "$message.payload#/id", or "".
	CorrelationIDLocation string
	// Bindings is the protocol bindings object after traits are merged in,
	// or nil.
	Bindings *Object
	Location Location
}

// Operation is one entry of the operations object.
type Operation struct {
	Name        string
	Action      Action
	Summary     string
	Description string
	Channel     *Channel
	Messages    []*Message
	Reply       *Reply
}

// Reply is the expected reply to an operation.
type Reply struct {
	Channel  *Channel
	Messages []*Message
	// AddressLocation is the runtime expression of a dynamic reply address,
	// such as "$message.header#/replyTo", or "".
	AddressLocation string
}

// Load reads the AsyncAPI document at path and every document it references.
func Load(path string) (*Document, error) {
	resolver := NewResolver()

	root, err := resolver.Document(path)
	if err != nil {
		return nil, err
	}

	rootObject, ok := root.(*Object)
	if !ok {
		return nil, ctxerrors.Wrapf(
			commerr.ErrParseFailed,
			"spec %s is not a mapping",
			path,
		)
	}

	version := rootObject.String("asyncapi")
	if !strings.HasPrefix(version, supportedMajorVersion) {
		return nil, ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"asyncapi version %q is not supported, only 3.x is",
			version,
		)
	}

	rootLocation, err := resolver.refLocation(path, Location{})
	if err != nil {
		return nil, err
	}

	rootLocation.Pointer = ""
	document := &Document{
		AsyncAPIVersion:    version,
		Resolver:           resolver,
		Root:               rootLocation,
		channelsByLocation: map[Location]*Channel{},
		messagesByLocation: map[Location]*Message{},
	}

	info := rootObject.Object("info")
	document.Title = info.String("title")
	document.Version = info.String("version")
	document.Description = info.String("description")
	document.DefaultContentType = rootObject.String("defaultContentType")

	if err := document.loadSections(rootObject); err != nil {
		return nil, err
	}

	return document, nil
}

// loadSections reads the servers, channels, operations and components of the
// root object. Operations come after channels because they refer to them.
func (d *Document) loadSections(rootObject *Object) error {
	if err := d.loadServers(rootObject.Object("servers")); err != nil {
		return err
	}

	if err := d.loadChannels(rootObject.Object("channels")); err != nil {
		return err
	}

	if err := d.loadOperations(rootObject.Object("operations")); err != nil {
		return err
	}

	return d.loadComponents(rootObject.Object("components"))
}

// SchemaComponent is one components.schemas entry.
type SchemaComponent struct {
	Name  string
	Value any
	At    Location
}

// loadComponents picks up component messages and schemas that no channel or
// operation references, so the reusable library in components is generated
// in full.
func (d *Document) loadComponents(components *Object) error {
	if components == nil {
		return nil
	}

	base := d.Root.Child("components")

	if messages := components.Object("messages"); messages != nil {
		messagesAt := base.Child("messages")
		for _, key := range messages.Keys {
			if _, err := d.message(
				"",
				key,
				messages.Values[key],
				messagesAt.Child(key),
			); err != nil {
				return ctxerrors.Wrapf(err, "component message %s", key)
			}
		}
	}

	if schemas := components.Object("schemas"); schemas != nil {
		schemasAt := base.Child("schemas")
		for _, name := range schemas.Keys {
			d.ComponentSchemas = append(d.ComponentSchemas, SchemaComponent{
				Name:  name,
				Value: schemas.Values[name],
				At:    schemasAt.Child(name),
			})
		}
	}

	return nil
}

func (d *Document) loadServers(servers *Object) error {
	if servers == nil {
		return nil
	}

	base := d.Root.Child("servers")
	for _, name := range servers.Keys {
		value, location, err := d.Resolver.Deref(
			servers.Values[name],
			base.Child(name),
		)
		if err != nil {
			return err
		}

		server, ok := value.(*Object)
		if !ok {
			return ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"server at %s is not a mapping",
				location,
			)
		}

		variables, err := d.loadServerVariables(
			server.Object("variables"),
			location.Child("variables"),
		)
		if err != nil {
			return ctxerrors.Wrapf(err, "server %s", name)
		}

		d.Servers = append(d.Servers, &Server{
			Name:            name,
			Host:            server.String("host"),
			Pathname:        server.String("pathname"),
			Protocol:        server.String("protocol"),
			ProtocolVersion: server.String("protocolVersion"),
			Description:     server.String("description"),
			Variables:       variables,
		})
	}

	return nil
}

func (d *Document) loadServerVariables(
	variables *Object,
	base Location,
) ([]*ServerVariable, error) {
	if variables == nil {
		return nil, nil
	}

	result := make([]*ServerVariable, 0, len(variables.Keys))
	for _, name := range variables.Keys {
		value, location, err := d.Resolver.Deref(
			variables.Values[name],
			base.Child(name),
		)
		if err != nil {
			return nil, err
		}

		object, ok := value.(*Object)
		if !ok {
			return nil, ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"server variable at %s is not a mapping",
				location,
			)
		}

		result = append(result, &ServerVariable{
			Name:        name,
			Default:     object.String("default"),
			Enum:        stringList(object.Values["enum"]),
			Description: object.String("description"),
		})
	}

	return result, nil
}

func stringList(value any) []string {
	items, _ := value.([]any)

	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}

	return result
}

func (d *Document) loadChannels(channels *Object) error {
	if channels == nil {
		return nil
	}

	base := d.Root.Child("channels")
	for _, name := range channels.Keys {
		value, location, err := d.Resolver.Deref(
			channels.Values[name],
			base.Child(name),
		)
		if err != nil {
			return err
		}

		object, ok := value.(*Object)
		if !ok {
			return ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"channel at %s is not a mapping",
				location,
			)
		}

		channel := &Channel{
			Name:        name,
			Address:     object.String("address"),
			Description: object.String("description"),
			Location:    location,
		}

		channel.Parameters, err = d.loadParameters(
			object.Object("parameters"),
			location.Child("parameters"),
		)
		if err != nil {
			return err
		}

		channel.Messages, err = d.loadMessages(
			name,
			object.Object("messages"),
			location.Child("messages"),
		)
		if err != nil {
			return err
		}

		d.Channels = append(d.Channels, channel)
		d.channelsByLocation[location] = channel
	}

	return nil
}

func (d *Document) loadParameters(
	parameters *Object,
	base Location,
) ([]*Parameter, error) {
	if parameters == nil {
		return nil, nil
	}

	result := make([]*Parameter, 0, len(parameters.Keys))
	for _, name := range parameters.Keys {
		value, location, err := d.Resolver.Deref(
			parameters.Values[name],
			base.Child(name),
		)
		if err != nil {
			return nil, err
		}

		object, ok := value.(*Object)
		if !ok {
			return nil, ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"parameter at %s is not a mapping",
				location,
			)
		}

		parameter := &Parameter{
			Name:        name,
			Description: object.String("description"),
			Default:     object.String("default"),
		}

		if values, ok := object.Values["enum"].([]any); ok {
			for _, enumValue := range values {
				if text, isText := enumValue.(string); isText {
					parameter.Enum = append(parameter.Enum, text)
				}
			}
		}

		result = append(result, parameter)
	}

	return result, nil
}

func (d *Document) loadMessages(
	channelName string,
	messages *Object,
	base Location,
) ([]*Message, error) {
	if messages == nil {
		return nil, nil
	}

	result := make([]*Message, 0, len(messages.Keys))
	for _, key := range messages.Keys {
		message, err := d.message(
			channelName,
			key,
			messages.Values[key],
			base.Child(key),
		)
		if err != nil {
			return nil, err
		}

		result = append(result, message)
	}

	return result, nil
}

// message returns the *Message for value, creating it the first time its
// definition is seen.
func (d *Document) message(
	channelName,
	key string,
	value any,
	at Location,
) (*Message, error) {
	resolved, location, err := d.Resolver.Deref(value, at)
	if err != nil {
		return nil, err
	}

	if existing, ok := d.messagesByLocation[location]; ok {
		return existing, nil
	}

	object, ok := resolved.(*Object)
	if !ok {
		return nil, ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"message at %s is not a mapping",
			location,
		)
	}

	messageKey := inlineMessageKey(channelName, key, location, at)

	object, err = d.Resolver.applyTraits(object, location)
	if err != nil {
		return nil, ctxerrors.Wrapf(err, "message %s", messageKey)
	}

	message := &Message{
		Key:         messageKey,
		Name:        fieldString(object, "name"),
		Title:       fieldString(object, "title"),
		Summary:     fieldString(object, "summary"),
		Description: fieldString(object, "description"),
		ContentType: firstText(
			fieldString(object, "contentType"),
			d.DefaultContentType,
		),
		Location: location,
	}

	message.Deprecated, _ = unwrapLocated(object.Values["deprecated"]).(bool)
	message.Bindings, _ = unwrapLocated(object.Values["bindings"]).(*Object)

	if err := d.loadMessageSchemas(message, object); err != nil {
		return nil, err
	}

	if err := d.loadCorrelationID(message, object); err != nil {
		return nil, err
	}

	d.messagesByLocation[location] = message
	d.Messages = append(d.Messages, message)

	return message, nil
}

// inlineMessageKey names a message. A message defined inline in a channel is
// prefixed with the channel name so equal keys in different channels differ.
func inlineMessageKey(
	channelName,
	key string,
	location,
	at Location,
) string {
	messageKey := location.LastToken()

	isInlineChannelMessage := location == at &&
		!isComponentMessage(location) &&
		key != ""
	if isInlineChannelMessage && channelName != "" {
		messageKey = channelName + "_" + key
	}

	return messageKey
}

// loadMessageSchemas resolves the payload and headers schemas of a message.
func (d *Document) loadMessageSchemas(
	message *Message,
	object *Object,
) error {
	var err error

	payloadValue, payloadAt := field(object, "payload", message.Location)

	message.Payload,
		message.PayloadAt,
		message.UnsupportedFormat,
		err = d.schema(payloadValue, payloadAt)
	if err != nil {
		return ctxerrors.Wrapf(err, "message %s payload", message.Key)
	}

	headersValue, headersAt := field(object, "headers", message.Location)

	message.Headers, message.HeadersAt, _, err = d.schema(
		headersValue,
		headersAt,
	)
	if err != nil {
		return ctxerrors.Wrapf(err, "message %s headers", message.Key)
	}

	return nil
}

// loadCorrelationID reads the runtime location of a message's correlation ID.
func (d *Document) loadCorrelationID(
	message *Message,
	object *Object,
) error {
	correlationValue, correlationAt := field(
		object,
		"correlationId",
		message.Location,
	)
	if correlationValue == nil {
		return nil
	}

	correlation, correlationLocation, err := d.Resolver.Deref(
		correlationValue,
		correlationAt,
	)
	if err != nil {
		return ctxerrors.Wrapf(err, "message %s correlationId", message.Key)
	}

	correlationObject, isObject := correlation.(*Object)
	if !isObject {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"correlationId at %s is not a mapping",
			correlationLocation,
		)
	}

	message.CorrelationIDLocation = correlationObject.String("location")

	return nil
}

// schema dereferences a payload or headers value and unwraps a Multi Format
// Schema Object. A schema in a format the generator cannot read comes back as
// nil with the format's name.
func (d *Document) schema(
	value any,
	at Location,
) (any, Location, string, error) {
	if value == nil {
		return nil, at, "", nil
	}

	resolved, location, err := d.Resolver.Deref(value, at)
	if err != nil {
		return nil, at, "", err
	}

	object, isObject := resolved.(*Object)
	if !isObject {
		return resolved, location, "", nil
	}

	schemaFormat, hasFormat := object.Values["schemaFormat"].(string)
	if !hasFormat {
		return resolved, location, "", nil
	}

	if !isJSONSchemaFormat(schemaFormat) {
		return nil, location, schemaFormat, nil
	}

	inner, innerAt, err := d.Resolver.Deref(
		object.Values["schema"],
		location.Child("schema"),
	)
	if err != nil {
		return nil, location, "", err
	}

	return inner, innerAt, "", nil
}

func firstText(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func isComponentMessage(location Location) bool {
	return strings.HasPrefix(location.Pointer, "/components/messages/")
}

func (d *Document) loadOperations(operations *Object) error {
	if operations == nil {
		return nil
	}

	base := d.Root.Child("operations")
	for _, name := range operations.Keys {
		operation, err := d.loadOperation(
			name,
			operations.Values[name],
			base.Child(name),
		)
		if err != nil {
			return err
		}

		d.Operations = append(d.Operations, operation)
	}

	return nil
}

func (d *Document) loadOperation(
	name string,
	rawValue any,
	at Location,
) (*Operation, error) {
	value, location, err := d.Resolver.Deref(rawValue, at)
	if err != nil {
		return nil, err
	}

	object, ok := value.(*Object)
	if !ok {
		return nil, ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"operation at %s is not a mapping",
			location,
		)
	}

	action := Action(object.String("action"))
	if action != ActionSend && action != ActionReceive {
		return nil, ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"operation %s has action %q, want %q or %q",
			name,
			action,
			ActionSend,
			ActionReceive,
		)
	}

	object, err = d.Resolver.applyTraits(object, location)
	if err != nil {
		return nil, ctxerrors.Wrapf(err, "operation %s", name)
	}

	operation := &Operation{
		Name:        name,
		Action:      action,
		Summary:     fieldString(object, "summary"),
		Description: fieldString(object, "description"),
	}

	if err := d.loadOperationTargets(operation, object, location); err != nil {
		return nil, err
	}

	return operation, nil
}

// loadOperationTargets resolves the channel, messages and optional reply of
// an operation.
func (d *Document) loadOperationTargets(
	operation *Operation,
	object *Object,
	location Location,
) error {
	var err error

	operation.Channel, err = d.channelRef(
		object.Values["channel"],
		location.Child("channel"),
	)
	if err != nil {
		return ctxerrors.Wrapf(err, "operation %s", operation.Name)
	}

	operation.Messages, err = d.messageRefs(
		operation.Channel,
		object.Values["messages"],
		location.Child("messages"),
	)
	if err != nil {
		return ctxerrors.Wrapf(err, "operation %s", operation.Name)
	}

	replyValue, replyAt := field(object, "reply", location)
	if replyValue == nil {
		return nil
	}

	operation.Reply, err = d.reply(operation.Channel, replyValue, replyAt)
	if err != nil {
		return ctxerrors.Wrapf(err, "operation %s reply", operation.Name)
	}

	return nil
}

func (d *Document) channelRef(value any, at Location) (*Channel, error) {
	if value == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "channel")
	}

	_, location, err := d.Resolver.Deref(value, at)
	if err != nil {
		return nil, err
	}

	channel, ok := d.channelsByLocation[location]
	if !ok {
		return nil, ctxerrors.Wrapf(commerr.ErrNotFound, "channel %s", location)
	}

	return channel, nil
}

// messageRefs resolves operation message references. An operation without a
// messages list uses every message of its channel.
func (d *Document) messageRefs(
	channel *Channel,
	value any,
	at Location,
) ([]*Message, error) {
	items, ok := value.([]any)
	if !ok {
		if channel == nil {
			return nil, nil
		}

		return channel.Messages, nil
	}

	channelName := ""
	if channel != nil {
		channelName = channel.Name
	}

	result := make([]*Message, 0, len(items))
	for i, item := range items {
		message, err := d.message(
			channelName,
			"",
			item,
			at.Child(strconv.Itoa(i)),
		)
		if err != nil {
			return nil, err
		}

		result = append(result, message)
	}

	return result, nil
}

// reply resolves an Operation Reply Object, which may itself be a $ref into
// components.replies. The value must not be nil. A reply without a channel
// replies on the operation's channel.
func (d *Document) reply(
	operationChannel *Channel,
	value any,
	at Location,
) (*Reply, error) {
	resolved, location, err := d.Resolver.Deref(value, at)
	if err != nil {
		return nil, err
	}

	reply, ok := resolved.(*Object)
	if !ok {
		return nil, ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"reply at %s is not a mapping",
			location,
		)
	}

	channel := operationChannel
	if channelValue, hasChannel := reply.Get("channel"); hasChannel {
		channel, err = d.channelRef(channelValue, location.Child("channel"))
		if err != nil {
			return nil, err
		}
	}

	messages, err := d.messageRefs(
		channel,
		reply.Values["messages"],
		location.Child("messages"),
	)
	if err != nil {
		return nil, err
	}

	result := &Reply{Channel: channel, Messages: messages}

	if addressValue, hasAddress := reply.Get("address"); hasAddress {
		address, addressAt, err := d.Resolver.Deref(
			addressValue,
			location.Child("address"),
		)
		if err != nil {
			return nil, err
		}

		addressObject, isObject := address.(*Object)
		if !isObject {
			return nil, ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"reply address at %s is not a mapping",
				addressAt,
			)
		}

		result.AddressLocation = addressObject.String("location")
	}

	return result, nil
}
