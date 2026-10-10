package agent

import (
	"strings"
	"text/template"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/harness"
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

	hookContextTemplateName  = "hook"
	eventHandlerTemplateName = "handler"
	escapeFunctionName       = "esc"

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
		Funcs(template.FuncMap{escapeFunctionName: escapeElement}).
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
