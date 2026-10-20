package oneshot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
)

const (
	// The Anthropic-style streaming frames carry the model's reply.
	frameTypeContentBlockDelta = "content_block_delta"
	deltaTypeText              = "text_delta"
	deltaTypeThinking          = "thinking_delta"

	frameTypeMessageFailed = "message.failed"

	// frameLineEndings are the characters a received frame may end with.
	frameLineEndings = "\r\n"

	// textSummaryLimit bounds how much of a tool call's arguments or a
	// failed tool's output one text-mode line shows.
	textSummaryLimit = 200

	textEllipsis = "..."
)

// OutputFormat selects how a run writes the session's frames.
type OutputFormat string

const (
	// OutputFormatJSONL writes every frame exactly as the controller sent
	// it, one JSON object per line, so another program can rebuild the
	// session's view from it.
	OutputFormatJSONL OutputFormat = "jsonl"

	// OutputFormatText writes the reply to stdout and the model's thinking,
	// tool calls, child agents, and failures to stderr.
	OutputFormatText OutputFormat = "text"
)

// ParseOutputFormat validates a format name.
func ParseOutputFormat(name string) (OutputFormat, error) {
	switch format := OutputFormat(name); format {
	case OutputFormatJSONL, OutputFormatText:
		return format, nil
	}

	return "", ctxerrors.Wrapf(
		commerr.ErrInvalidValue,
		"output format %q is not %s or %s",
		name,
		OutputFormatJSONL,
		OutputFormatText,
	)
}

// renderer writes the frames of one run.
type renderer interface {
	render(frame controlclient.TurnFrame) error
	finish() error
}

//nolint:ireturn // The format selects one of two renderer implementations.
func newRenderer(format OutputFormat, stdout, stderr io.Writer) renderer {
	if format == OutputFormatText {
		return &textRenderer{
			stdout:            stdout,
			stderr:            stderr,
			isStdoutAtLineEnd: true,
			isStderrAtLineEnd: true,
		}
	}

	return &jsonlRenderer{stdout: stdout}
}

type jsonlRenderer struct {
	stdout io.Writer
}

func (r *jsonlRenderer) render(frame controlclient.TurnFrame) error {
	// The hub ends each frame with a newline. Trimming it keeps one frame
	// per line instead of a frame followed by an empty line.
	raw := bytes.TrimRight(frame.Raw, frameLineEndings)
	line := make([]byte, 0, len(raw)+1)
	line = append(line, raw...)
	line = append(line, '\n')

	if _, err := r.stdout.Write(line); err != nil {
		return ctxerrors.Wrap(err, "write a frame")
	}

	return nil
}

func (r *jsonlRenderer) finish() error {
	return nil
}

// textRenderer tracks whether each stream ends a line, so an event line never
// starts in the middle of streamed thinking or reply text.
type textRenderer struct {
	stdout            io.Writer
	stderr            io.Writer
	isStdoutAtLineEnd bool
	isStderrAtLineEnd bool
}

//nolint:cyclop // Each supported wire frame has its own rendering branch.
func (r *textRenderer) render(frame controlclient.TurnFrame) error {
	switch frame.Type {
	case frameTypeContentBlockDelta:
		return r.renderDelta(frame.Data)
	case agent.EventTypeToolUse:
		return r.renderToolUse(frame.Data)
	case agent.EventTypeToolResult:
		return r.renderToolResult(frame.Data)
	case agent.EventTypeProviderRetry:
		return r.renderProviderRetry(frame.Data)
	case agent.EventTypeQuestionAsked:
		return r.renderQuestion(frame.Data)
	case agent.EventTypeAgentRunStarted:
		return r.renderAgentRunStarted(frame.Data)
	case agent.EventTypeAgentRunCompleted,
		agent.EventTypeAgentRunFailed,
		agent.EventTypeAgentRunCancelled:
		return r.renderAgentRunEnded(frame)
	case agent.EventTypeTurnFailed:
		return r.renderTurnFailed(frame.Data)
	case agent.EventTypeTurnCancelled:
		return r.line("turn cancelled")
	case frameTypeMessageFailed:
		return r.renderMessageFailed(frame.Data)
	}

	return nil
}

func (r *textRenderer) finish() error {
	if r.isStdoutAtLineEnd {
		return nil
	}

	return r.writeStdout("\n")
}

// textDelta is the part of a content_block_delta frame text mode reads.
type textDelta struct {
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

func (r *textRenderer) renderDelta(data json.RawMessage) error {
	delta := textDelta{}
	if err := json.Unmarshal(data, &delta); err != nil {
		return ctxerrors.Wrap(err, "decode a content block delta")
	}

	switch delta.Delta.Type {
	case deltaTypeText:
		return r.writeStdout(delta.Delta.Text)
	case deltaTypeThinking:
		return r.writeStderr(delta.Delta.Text)
	}

	return nil
}

func (r *textRenderer) renderToolUse(data json.RawMessage) error {
	use := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{}
	if err := json.Unmarshal(data, &use); err != nil {
		return ctxerrors.Wrap(err, "decode a tool call")
	}

	return r.line(fmt.Sprintf(
		"> %s %s",
		use.Name,
		summarize(string(use.Arguments)),
	))
}

func (r *textRenderer) renderToolResult(data json.RawMessage) error {
	result := wsapi.ToolResult{}
	if err := json.Unmarshal(data, &result); err != nil {
		return ctxerrors.Wrap(err, "decode a tool result")
	}

	if !result.IsError {
		return nil
	}

	return r.line(fmt.Sprintf(
		"! %s failed: %s",
		result.Name,
		summarize(result.Content),
	))
}

func (r *textRenderer) renderProviderRetry(data json.RawMessage) error {
	retry := wsapi.ProviderRetry{}
	if err := json.Unmarshal(data, &retry); err != nil {
		return ctxerrors.Wrap(err, "decode a provider retry")
	}

	return r.line(fmt.Sprintf(
		"provider retry %d: %s, waiting %dms",
		retry.Attempt,
		retry.Reason,
		retry.DelayMs,
	))
}

func (r *textRenderer) renderQuestion(data json.RawMessage) error {
	question := wsapi.QuestionAsked{}
	if err := json.Unmarshal(data, &question); err != nil {
		return ctxerrors.Wrap(err, "decode a question")
	}

	return r.line("? " + question.Question)
}

func (r *textRenderer) renderAgentRunStarted(data json.RawMessage) error {
	started := struct {
		AgentRunID string `json:"agentRunId"`
		Event      struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"event"`
	}{}
	if err := json.Unmarshal(data, &started); err != nil {
		return ctxerrors.Wrap(err, "decode a child agent start")
	}

	return r.line(fmt.Sprintf(
		"agent %s started: %s on %s",
		started.AgentRunID,
		started.Event.Name,
		started.Event.Model,
	))
}

func (r *textRenderer) renderAgentRunEnded(
	frame controlclient.TurnFrame,
) error {
	ended := struct {
		AgentRunID string `json:"agentRunId"`
	}{}
	if err := json.Unmarshal(frame.Data, &ended); err != nil {
		return ctxerrors.Wrap(err, "decode a child agent end")
	}

	return r.line(fmt.Sprintf(
		"agent %s %s",
		ended.AgentRunID,
		strings.TrimPrefix(frame.Type, agentRunTypePrefix),
	))
}

func (r *textRenderer) renderTurnFailed(data json.RawMessage) error {
	failed := wsapi.TurnFailedData{}
	if err := json.Unmarshal(data, &failed); err != nil {
		return ctxerrors.Wrap(err, "decode a turn failure")
	}

	return r.line("turn failed: " + failed.Reason)
}

func (r *textRenderer) renderMessageFailed(data json.RawMessage) error {
	failed := wsapi.MessageFailedData{}
	if err := json.Unmarshal(data, &failed); err != nil {
		return ctxerrors.Wrap(err, "decode a message failure")
	}

	return r.line(fmt.Sprintf(
		"message failed: %s: %s",
		failed.Code,
		failed.Message,
	))
}

// line writes one event line to stderr, first ending any streamed text that
// is still open on either stream.
func (r *textRenderer) line(text string) error {
	if !r.isStdoutAtLineEnd {
		if err := r.writeStdout("\n"); err != nil {
			return err
		}
	}

	if !r.isStderrAtLineEnd {
		if err := r.writeStderr("\n"); err != nil {
			return err
		}
	}

	return r.writeStderr(text + "\n")
}

func (r *textRenderer) writeStdout(text string) error {
	if text == "" {
		return nil
	}

	if _, err := io.WriteString(r.stdout, text); err != nil {
		return ctxerrors.Wrap(err, "write the reply")
	}

	r.isStdoutAtLineEnd = strings.HasSuffix(text, "\n")

	return nil
}

func (r *textRenderer) writeStderr(text string) error {
	if text == "" {
		return nil
	}

	if _, err := io.WriteString(r.stderr, text); err != nil {
		return ctxerrors.Wrap(err, "write an event")
	}

	r.isStderrAtLineEnd = strings.HasSuffix(text, "\n")

	return nil
}

// summarize keeps the first line of text, cut to textSummaryLimit bytes on a
// rune boundary.
func summarize(text string) string {
	firstLine, _, isCut := strings.Cut(text, "\n")
	if len(firstLine) > textSummaryLimit {
		firstLine = strings.ToValidUTF8(firstLine[:textSummaryLimit], "")
		isCut = true
	}

	if isCut {
		return firstLine + textEllipsis
	}

	return firstLine
}
