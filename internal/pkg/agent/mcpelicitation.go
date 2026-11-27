package agent

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem/mcp"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const (
	// mcpElicitationAttempts is how many replies one question takes before
	// an answer that never fits the form is declined.
	mcpElicitationAttempts = 3
	// mcpElicitationMessageMaxBytes bounds the server's message, and
	// mcpElicitationFieldMaxBytes each field title and description, in the
	// question the user sees.
	mcpElicitationMessageMaxBytes = 4096
	mcpElicitationFieldMaxBytes   = 256
	// mcpElicitationMaxFields bounds the fields a form may ask for.
	mcpElicitationMaxFields = 32

	mcpElicitationReplyDecline = "decline"
	mcpElicitationReplyCancel  = "cancel"
	mcpElicitationListSplit    = ","
	mcpElicitationJSONStart    = "{"
	mcpElicitationEnumJoin     = ", "

	mcpElicitationQuestionTemplateName = "mcpElicitation"
	//nolint:lll // One user-facing question.
	mcpElicitationQuestionTemplateText = `The MCP server {{.Server}} asks a question. The text between the lines is the server's own, not Peen's or the agent's.
---
{{.Message}}
---
{{if .Single}}Reply with the value for {{.Single}}, or with a JSON object of the fields.{{else}}Reply with a JSON object of the fields.{{end}} Reply decline to refuse or cancel to skip. Your reply also reaches the agent.
{{range .Fields}}
- {{.Name}} ({{.Type}}{{if .Required}}, required{{end}}){{if .Choices}}, one of: {{.Choices}}{{end}}{{if .Description}}: {{.Description}}{{end}}{{end}}{{if .Problem}}

Your previous reply was not used: {{.Problem}}{{end}}`

	mcpSchemaTypeString  = "string"
	mcpSchemaTypeNumber  = "number"
	mcpSchemaTypeInteger = "integer"
	mcpSchemaTypeBoolean = "boolean"
	mcpSchemaTypeArray   = "array"

	// The reasons a question is declined without asking anyone, for the log.
	mcpReasonURLMode        = "url_mode"
	mcpReasonNoActiveTurn   = "no_active_turn"
	mcpReasonNotInteractive = "not_interactive"
	mcpReasonBadSchema      = "unsupported_schema"
)

// mcpElicitationSchema is a form a server asks the user to fill in: a flat
// object of strings, numbers, integers, booleans, and choices.
type mcpElicitationSchema struct {
	Properties map[string]mcpElicitationProperty `json:"properties"`
	Required   []string                          `json:"required"`
}

type mcpElicitationProperty struct {
	Type        string               `json:"type"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	Enum        []string             `json:"enum"`
	OneOf       []mcpElicitationEnum `json:"oneOf"`
	MinLength   *int                 `json:"minLength"`
	MaxLength   *int                 `json:"maxLength"`
	Minimum     *float64             `json:"minimum"`
	Maximum     *float64             `json:"maximum"`
	Items       *mcpElicitationItems `json:"items"`
	MinItems    *int                 `json:"minItems"`
	MaxItems    *int                 `json:"maxItems"`
}

type mcpElicitationEnum struct {
	Const string `json:"const"`
}

type mcpElicitationItems struct {
	Enum  []string             `json:"enum"`
	AnyOf []mcpElicitationEnum `json:"anyOf"`
}

// choices are the values a property allows, or none when it allows any.
func (p mcpElicitationProperty) choices() []string {
	if p.Type == mcpSchemaTypeArray && p.Items != nil {
		return enumValues(p.Items.Enum, p.Items.AnyOf)
	}

	return enumValues(p.Enum, p.OneOf)
}

func enumValues(values []string, options []mcpElicitationEnum) []string {
	all := slices.Clone(values)
	for _, option := range options {
		all = append(all, option.Const)
	}

	return all
}

// mcpElicitationField is one form field as the question shows it.
type mcpElicitationField struct {
	Name        string
	Type        string
	Required    bool
	Choices     string
	Description string
}

// mcpElicitationQuestion is what mcpElicitationQuestionTemplateText renders.
type mcpElicitationQuestion struct {
	Server  string
	Message string
	Single  string
	Fields  []mcpElicitationField
	Problem string
}

// elicitMCP asks the user an MCP server's form question through the turn's
// question flow and returns the answer checked against the form. A question
// no person can answer is declined: there is no running turn, the turn came
// from a caller that waits for its result or from an event, or the server
// asks the user to open a URL. A reply that never fits the form is declined
// after mcpElicitationAttempts tries, and a question nobody answers within
// PEEN_ASK_TIMEOUT, or before the turn ends, is cancelled.
func (r *Runtime) elicitMCP(
	ctx context.Context,
	server string,
	request mcp.ElicitRequest,
) (mcp.ElicitResult, error) {
	logger := runtimecontext.Logger(ctx)
	declined := mcp.ElicitResult{Action: mcp.ElicitActionDecline}

	reason, queue, schema := r.mcpElicitationTarget(request)
	if reason != "" {
		logger.Info("MCP elicitation declined",
			"server", server, "reason", reason)

		return declined, nil
	}

	r.mcpElicitations.Add(1)

	question := newMCPElicitationQuestion(server, request.Message, schema)
	callID := parentToolCallIDFromContext(ctx)

	for range mcpElicitationAttempts {
		reply, answered, err := r.askMCPQuestion(ctx, queue, question, callID)
		if err != nil || !answered {
			return mcp.ElicitResult{Action: mcp.ElicitActionCancel}, err
		}

		result, problem := parseMCPElicitationReply(reply, schema)
		if problem == "" {
			logger.Info("MCP elicitation answered",
				"server", server, "action", result.Action)

			return result, nil
		}

		question.Problem = problem
	}

	logger.Info("MCP elicitation declined",
		"server", server, "reason", "reply_never_fit_the_form")

	return declined, nil
}

// mcpElicitationTarget is the queue that takes the user's reply and the
// form to check it against, or why the question is declined unasked.
func (r *Runtime) mcpElicitationTarget(
	request mcp.ElicitRequest,
) (string, *activeUserMessageQueue, mcpElicitationSchema) {
	var schema mcpElicitationSchema

	if request.Mode != mcp.ElicitModeForm {
		return mcpReasonURLMode, nil, schema
	}

	r.userMessageQueuesMutex.Lock()
	queue := r.userMessageQueues[r.sessionID]
	r.userMessageQueuesMutex.Unlock()

	if queue == nil || r.sessionID == uuid.Nil {
		return mcpReasonNoActiveTurn, nil, schema
	}

	if !queue.interactive {
		return mcpReasonNotInteractive, nil, schema
	}

	if json.Unmarshal(request.Schema, &schema) != nil ||
		len(schema.Properties) > mcpElicitationMaxFields {
		return mcpReasonBadSchema, nil, schema
	}

	return "", queue, schema
}

// askMCPQuestion shows the question on the turn's event stream and waits
// for the next message the user sends. answered is false when nobody
// replied in time or the turn ended.
func (r *Runtime) askMCPQuestion(
	ctx context.Context,
	queue *activeUserMessageQueue,
	question mcpElicitationQuestion,
	callID string,
) (string, bool, error) {
	text, err := renderTemplate(mcpElicitationQuestionTemplateName,
		mcpElicitationQuestionTemplateText, question)
	if err != nil {
		return "", false, err
	}

	replies, stop := queue.awaitAnswer()
	defer stop()

	if err := queue.turn.emit(ctx, EventTypeQuestionAsked,
		agentQuestionPayload{Question: text, CallID: callID}); err != nil {
		return "", false, ctxerrors.Wrap(err, "record MCP question")
	}

	reply, answered := r.waitMCPReply(ctx, queue, replies)

	if err := queue.turn.emit(context.WithoutCancel(ctx),
		EventTypeQuestionAnswered,
		agentAnswerPayload{Answered: answered, CallID: callID}); err != nil {
		return "", false, ctxerrors.Wrap(err, "record MCP answer")
	}

	return reply, answered, nil
}

// waitMCPReply waits for a reply until PEEN_ASK_TIMEOUT, the turn's end, or
// ctx's end. A question from a server on an older protocol arrives on the
// server's own request, whose ctx outlives the tool call, so the turn's end
// bounds it too.
func (r *Runtime) waitMCPReply(
	ctx context.Context,
	queue *activeUserMessageQueue,
	replies <-chan string,
) (string, bool) {
	var expired <-chan time.Time

	if r.askTimeout > 0 {
		timer := time.NewTimer(r.askTimeout)
		defer timer.Stop()

		expired = timer.C
	}

	select {
	case reply := <-replies:
		return reply, true
	case <-expired:
	case <-queue.retired:
	case <-ctx.Done():
	}

	return "", false
}

// awaitAnswer registers for the text of the next message queued. stop
// unregisters it.
func (q *activeUserMessageQueue) awaitAnswer() (<-chan string, func()) {
	reply := make(chan string, 1)

	q.mutex.Lock()
	q.answerWaiters = append(q.answerWaiters, reply)
	q.mutex.Unlock()

	return reply, func() {
		q.mutex.Lock()
		defer q.mutex.Unlock()

		q.answerWaiters = slices.DeleteFunc(q.answerWaiters,
			func(waiter chan string) bool { return waiter == reply })
	}
}

// handAnswer gives a queued message's text to the oldest waiter. The
// caller holds q.mutex.
func (q *activeUserMessageQueue) handAnswer(message string) {
	if len(q.answerWaiters) == 0 {
		return
	}

	waiter := q.answerWaiters[0]
	q.answerWaiters = q.answerWaiters[1:]

	waiter <- message
}

func newMCPElicitationQuestion(
	server, message string,
	schema mcpElicitationSchema,
) mcpElicitationQuestion {
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}

	slices.Sort(names)

	question := mcpElicitationQuestion{
		Server: server,
		Message: workflowUTF8Prefix(strings.TrimSpace(message),
			mcpElicitationMessageMaxBytes),
		Fields: make([]mcpElicitationField, 0, len(names)),
	}

	if len(names) == 1 {
		question.Single = names[0]
	}

	for _, name := range names {
		property := schema.Properties[name]
		description := strings.TrimSpace(strings.Join([]string{
			property.Title, property.Description,
		}, " "))

		question.Fields = append(question.Fields, mcpElicitationField{
			Name: name, Type: property.Type,
			Required: slices.Contains(schema.Required, name),
			Choices: strings.Join(property.choices(),
				mcpElicitationEnumJoin),
			Description: workflowUTF8Prefix(description,
				mcpElicitationFieldMaxBytes),
		})
	}

	return question
}

// parseMCPElicitationReply turns the user's reply into an answer, or says
// what is wrong with it.
func parseMCPElicitationReply(
	reply string,
	schema mcpElicitationSchema,
) (mcp.ElicitResult, string) {
	trimmed := strings.TrimSpace(reply)

	switch strings.ToLower(trimmed) {
	case mcpElicitationReplyDecline:
		return mcp.ElicitResult{Action: mcp.ElicitActionDecline}, ""
	case mcpElicitationReplyCancel:
		return mcp.ElicitResult{Action: mcp.ElicitActionCancel}, ""
	}

	content, problem := mcpElicitationContent(trimmed, schema)
	if problem != "" {
		return mcp.ElicitResult{}, problem
	}

	problem = validateMCPElicitationContent(content, schema)
	if problem != "" {
		return mcp.ElicitResult{}, problem
	}

	return mcp.ElicitResult{
		Action: mcp.ElicitActionAccept, Content: content,
	}, ""
}

// mcpElicitationContent reads a JSON object reply, or the bare value of a
// one-field form.
func mcpElicitationContent(
	reply string,
	schema mcpElicitationSchema,
) (map[string]any, string) {
	if strings.HasPrefix(reply, mcpElicitationJSONStart) {
		var content map[string]any
		if err := json.Unmarshal([]byte(reply), &content); err != nil {
			return nil, "it is not a JSON object: " + err.Error()
		}

		return content, ""
	}

	if len(schema.Properties) != 1 {
		return nil, "this form has several fields, so reply with a JSON object"
	}

	for name, property := range schema.Properties {
		value, problem := mcpElicitationScalar(reply, property)
		if problem != "" {
			return nil, name + " " + problem
		}

		return map[string]any{name: value}, ""
	}

	return nil, "the form has no fields"
}

// mcpElicitationScalar reads a bare reply as a value of the property's type.
func mcpElicitationScalar(
	reply string,
	property mcpElicitationProperty,
) (any, string) {
	switch property.Type {
	case mcpSchemaTypeNumber, mcpSchemaTypeInteger:
		number, err := strconv.ParseFloat(reply, 64)
		if err != nil {
			return nil, "must be a number"
		}

		return number, ""
	case mcpSchemaTypeBoolean:
		value, err := strconv.ParseBool(reply)
		if err != nil {
			return nil, "must be true or false"
		}

		return value, ""
	case mcpSchemaTypeArray:
		items := make([]any, 0)

		for item := range strings.SplitSeq(reply, mcpElicitationListSplit) {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}

		return items, ""
	default:
		return reply, ""
	}
}

// validateMCPElicitationContent checks an answer against the form: known
// fields only, every required field, and each value's type and bounds.
func validateMCPElicitationContent(
	content map[string]any,
	schema mcpElicitationSchema,
) string {
	for name := range content {
		if _, known := schema.Properties[name]; !known {
			return "the form has no field " + strconv.Quote(name)
		}
	}

	for _, name := range schema.Required {
		if _, given := content[name]; !given {
			return "the required field " + strconv.Quote(name) + " is missing"
		}
	}

	for name, value := range content {
		if problem := validateMCPElicitationValue(value,
			schema.Properties[name]); problem != "" {
			return name + " " + problem
		}
	}

	return ""
}

func validateMCPElicitationValue(
	value any,
	property mcpElicitationProperty,
) string {
	switch property.Type {
	case mcpSchemaTypeString:
		return validateMCPElicitationString(value, property)
	case mcpSchemaTypeNumber, mcpSchemaTypeInteger:
		return validateMCPElicitationNumber(value, property)
	case mcpSchemaTypeBoolean:
		if _, isBool := value.(bool); !isBool {
			return "must be true or false"
		}

		return ""
	case mcpSchemaTypeArray:
		return validateMCPElicitationList(value, property)
	default:
		return "has a type the form cannot take"
	}
}

func validateMCPElicitationString(
	value any,
	property mcpElicitationProperty,
) string {
	text, isText := value.(string)
	if !isText {
		return "must be text"
	}

	if choices := property.choices(); len(choices) > 0 &&
		!slices.Contains(choices, text) {
		return "must be one of: " + strings.Join(choices,
			mcpElicitationEnumJoin)
	}

	length := len([]rune(text))
	if property.MinLength != nil && length < *property.MinLength {
		return "is shorter than " + strconv.Itoa(*property.MinLength) +
			" characters"
	}

	if property.MaxLength != nil && length > *property.MaxLength {
		return "is longer than " + strconv.Itoa(*property.MaxLength) +
			" characters"
	}

	return ""
}

func validateMCPElicitationNumber(
	value any,
	property mcpElicitationProperty,
) string {
	number, isNumber := value.(float64)
	if !isNumber {
		return "must be a number"
	}

	if property.Type == mcpSchemaTypeInteger && number != math.Trunc(number) {
		return "must be a whole number"
	}

	if property.Minimum != nil && number < *property.Minimum {
		return "is below " + strconv.FormatFloat(*property.Minimum, 'g', -1, 64)
	}

	if property.Maximum != nil && number > *property.Maximum {
		return "is above " + strconv.FormatFloat(*property.Maximum, 'g', -1, 64)
	}

	return ""
}

func validateMCPElicitationList(
	value any,
	property mcpElicitationProperty,
) string {
	items, isList := value.([]any)
	if !isList {
		return "must be a list"
	}

	choices := property.choices()

	for _, item := range items {
		text, isText := item.(string)
		if !isText || (len(choices) > 0 && !slices.Contains(choices, text)) {
			return "must list only: " + strings.Join(choices,
				mcpElicitationEnumJoin)
		}
	}

	if property.MinItems != nil && len(items) < *property.MinItems {
		return "needs at least " + strconv.Itoa(*property.MinItems) + " items"
	}

	if property.MaxItems != nil && len(items) > *property.MaxItems {
		return "takes at most " + strconv.Itoa(*property.MaxItems) + " items"
	}

	return ""
}
