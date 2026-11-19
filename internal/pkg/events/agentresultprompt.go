package events

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

const (
	// AgentMessageSenderAgent is the from attribute of a <message> block an
	// agent sent.
	AgentMessageSenderAgent = "agent"
	// AgentMessageKindResult, AgentMessageKindFailed, and
	// AgentMessageKindCancelled are the kind attribute of the <message> block
	// that carries how a child agent ended.
	AgentMessageKindResult    = "result"
	AgentMessageKindFailed    = "failed"
	AgentMessageKindCancelled = "cancelled"

	agentResultTemplateName = "agentResult"
	agentResultTemplateText = `<message from="{{.From}}" name="{{.Name}}" ` +
		`run="{{.Run}}" kind="{{.Kind}}">
{{.Body}}
</message>`
	// agentResultFailedFormat is the body of a child that did not complete.
	// It takes the run state and the failure detail.
	agentResultFailedFormat = "The agent did not finish (%s): %s"
)

// agentResultCloseEscaper keeps a child's answer from closing its message
// block early, the same way agent messages are escaped in the agent package.
//
//nolint:gochecknoglobals // Stateless replacer.
var agentResultCloseEscaper = strings.NewReplacer("</message", "&lt;/message")

type agentResultView struct {
	From string
	Name string
	Run  string
	Kind string
	Body string
}

// agentResultMessage renders an agent.finished or agent.failed notice as the
// <message> block a parent agent reads. It reports false for any other
// notice and for one whose data cannot be read, which then keeps its JSON
// form inside the events block.
func agentResultMessage(notice Notice) (string, bool) {
	if notice.Type != TypeAgentFinished && notice.Type != TypeAgentFailed {
		return "", false
	}

	var data AgentResultData
	if err := json.Unmarshal(notice.Data, &data); err != nil ||
		data.AgentName == "" {
		return "", false
	}

	rendered, err := renderAgentResult(agentResultView{
		From: AgentMessageSenderAgent,
		Name: data.AgentName,
		Run:  data.RunID.String(),
		Kind: agentResultKind(data.State),
		Body: agentResultCloseEscaper.Replace(agentResultBody(data)),
	})
	if err != nil {
		return "", false
	}

	return rendered, true
}

func renderAgentResult(view agentResultView) (string, error) {
	parsed, err := template.New(agentResultTemplateName).
		Parse(agentResultTemplateText)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse agent result template")
	}

	builder := &strings.Builder{}
	if err := parsed.Execute(builder, view); err != nil {
		return "", ctxerrors.Wrap(err, "render agent result")
	}

	return builder.String(), nil
}

func agentResultKind(state string) string {
	switch models.AgentRunState(state) {
	case models.AgentRunStateCompleted:
		return AgentMessageKindResult
	case models.AgentRunStateCancelled:
		return AgentMessageKindCancelled
	case models.AgentRunStateRunning, models.AgentRunStateFailed,
		models.AgentRunStateInterrupted:
		return AgentMessageKindFailed
	default:
		return AgentMessageKindFailed
	}
}

func agentResultBody(data AgentResultData) string {
	if models.AgentRunState(data.State) == models.AgentRunStateCompleted {
		return data.Response
	}

	return fmt.Sprintf(agentResultFailedFormat, data.State, data.FailureDetail)
}
