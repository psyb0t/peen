package agent

import (
	"strings"
	"text/template"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	// hookContextTemplateText is what a hook inject action reaches the model
	// as, in the same shape as a skill message: metadata elements, a blank
	// line, then the body. The tool element is present only for a tool event.
	// The body is the user's own configuration, so it is not escaped.
	hookContextTemplateText = `<hook>
<event>{{esc .Event}}</event>{{if .Tool}}
<tool>{{esc .Tool}}</tool>{{end}}

{{.Text}}
</hook>`

	// eventHandlerTemplateText is the first message of a turn an event
	// handler started. The agent element and sentence are present only when
	// the handler names an agent.
	eventHandlerTemplateText = `<handler>
<event>{{esc .Type}}</event>{{if .Agent}}
<agent>{{esc .Agent}}</agent>{{end}}

{{.Instructions}}{{if .Agent}}

Handle this as the {{.Agent}} agent would.{{end}}
</handler>`

	// agentMessageTemplateText is a message one participant in a session
	// sends another: a child's task, a message between agents, or the user
	// writing to a child. The attributes name the sender. The body is the
	// sender's own text and stays as written, because agents exchange code.
	//
	//nolint:lll // The opening tag is one line in the rendered message.
	agentMessageTemplateText = `<message from="{{attr .From}}"{{if .Name}} name="{{attr .Name}}"{{end}}{{if .Run}} run="{{attr .Run}}"{{end}}{{if .Kind}} kind="{{attr .Kind}}"{{end}}>
{{.Text}}
</message>`

	// pausedNoticeTemplateText is what a paused child reads ahead of the
	// message that resumes it. The reason is Peen's own pause reason, which
	// can carry an error, so it is escaped.
	//
	//nolint:lll // The sentence is one line in the rendered notice.
	pausedNoticeTemplateText = `<paused>
<reason>{{esc .Reason}}</reason>

Your work was paused for the reason above, and the message after this one resumes it. Check the workspace before you continue, because it may have changed while you were paused.
</paused>`

	hookContextTemplateName  = "hook"
	eventHandlerTemplateName = "handler"
	agentMessageTemplateName = "message"
	pausedNoticeTemplateName = "paused"
	escapeFunctionName       = "esc"
	attributeFunctionName    = "attr"

	// messageSenderUser and messageSenderAgent are the from values of a
	// message block.
	messageSenderUser  = "user"
	messageSenderAgent = session.AgentMessageSenderAgent
	// rootAgentRunReference stands in for a run ID when the sender or
	// recipient is the session's root conversation.
	rootAgentRunReference = "root"

	// messageKindQuestion marks a message that asks something.
	// messageKindResult, messageKindFailed, and messageKindCancelled mark the
	// message carrying how a child ended.
	messageKindQuestion  = "question"
	messageKindResult    = "result"
	messageKindFailed    = "failed"
	messageKindCancelled = "cancelled"

	hookContextTextSeparator = "\n\n"
)

// hookContextData is what hookContextTemplateText renders.
type hookContextData struct {
	Event string
	Tool  string
	Text  string
}

// eventHandlerData is what eventHandlerTemplateText renders.
type eventHandlerData struct {
	Type         string
	Agent        string
	Instructions string
}

// elementEscaper escapes a metadata value so its text cannot close the
// element early.
//
//nolint:gochecknoglobals // Immutable lookup table.
var elementEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

func escapeElement(value string) string {
	return elementEscaper.Replace(value)
}

// attributeEscaper escapes a value placed inside a double-quoted attribute,
// so a sender name cannot end the attribute or the tag.
//
//nolint:gochecknoglobals // Immutable lookup table.
var attributeEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
)

func escapeAttribute(value string) string {
	return attributeEscaper.Replace(value)
}

// agentMessage is what agentMessageTemplateText renders. Name and Run are
// empty for a message from the user. Kind is empty for a plain message.
type agentMessage struct {
	From string
	Name string
	Run  string
	Kind string
	Text string
}

// renderAgentMessage wraps one message in a block naming its sender.
func renderAgentMessage(message agentMessage) (string, error) {
	return renderTemplate(
		agentMessageTemplateName,
		agentMessageTemplateText,
		message,
	)
}

// pausedNoticeData is what pausedNoticeTemplateText renders.
type pausedNoticeData struct {
	Reason string
}

// renderPausedNotice tells a paused child why it was paused, ahead of the
// message that resumes it.
func renderPausedNotice(reason string) (string, error) {
	return renderTemplate(
		pausedNoticeTemplateName,
		pausedNoticeTemplateText,
		pausedNoticeData{Reason: reason},
	)
}

// renderHookContext wraps the strings one hook event injected in a single
// hook block. tool is empty for a lifecycle event.
func renderHookContext(
	event harness.HookEvent,
	tool string,
	messages []string,
) (string, error) {
	return renderTemplate(
		hookContextTemplateName,
		hookContextTemplateText,
		hookContextData{
			Event: string(event),
			Tool:  tool,
			Text:  strings.Join(messages, hookContextTextSeparator),
		},
	)
}

// renderEventHandler is the message a handler-started turn begins with: the
// handler's instructions in a handler block. The event itself is not
// pasted here. It is queued and reaches the turn as quoted data, which keeps
// event text out of the instruction.
func renderEventHandler(handler harness.EventHandler) (string, error) {
	return renderTemplate(
		eventHandlerTemplateName,
		eventHandlerTemplateText,
		eventHandlerData{
			Type:         handler.Type,
			Agent:        handler.Agent,
			Instructions: strings.TrimSpace(handler.Instructions),
		},
	)
}

func renderTemplate(
	name string,
	text string,
	data any,
) (string, error) {
	parsed, err := template.New(name).
		Funcs(template.FuncMap{
			escapeFunctionName:    escapeElement,
			attributeFunctionName: escapeAttribute,
		}).
		Parse(text)
	if err != nil {
		return "", ctxerrors.Wrapf(err, "parse %s template", name)
	}

	var rendered strings.Builder

	if err := parsed.Execute(&rendered, data); err != nil {
		return "", ctxerrors.Wrapf(err, "render %s template", name)
	}

	return rendered.String(), nil
}
