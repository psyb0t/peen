package oneshot

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONLRendererPreservesRawFrames(t *testing.T) {
	t.Parallel()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	output := newRenderer(OutputFormatJSONL, stdout, stderr)
	raw := json.RawMessage(`{ "type": "custom", "data": {"a":1} }`)
	require.NoError(t, output.render(controlclient.TurnFrame{Raw: raw}))
	require.NoError(t, output.finish())
	assert.Equal(t, string(raw)+"\n", stdout.String())
	assert.Empty(t, stderr.String())
}

func TestJSONLRendererWritesOneLinePerNewlineEndedFrame(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		suffix string
	}{
		{name: "line feed", suffix: "\n"},
		{name: "carriage return line feed", suffix: "\r\n"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			output := newRenderer(OutputFormatJSONL, stdout, stderr)
			frames := []string{`{"type":"a"}`, `{"type":"b"}`}

			for _, frame := range frames {
				require.NoError(t, output.render(controlclient.TurnFrame{
					Raw: json.RawMessage(frame + tc.suffix),
				}))
			}

			require.NoError(t, output.finish())
			assert.Equal(
				t,
				strings.Join(frames, "\n")+"\n",
				stdout.String(),
			)
			assert.Empty(t, stderr.String())
		})
	}
}

func TestTextRendererRoutesStreamsAndEndsLines(t *testing.T) {
	t.Parallel()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	output := newRenderer(OutputFormatText, stdout, stderr)
	frames := []controlclient.TurnFrame{
		{
			Type: frameTypeContentBlockDelta,
			Data: json.RawMessage(`{"delta":{"type":"text_delta","text":"reply"}}`),
		},
		{
			Type: frameTypeContentBlockDelta,
			Data: json.RawMessage(`{"delta":{"type":"thinking_delta","text":"think"}}`),
		},
		{
			Type: agent.EventTypeToolUse,
			Data: json.RawMessage(`{"name":"readFile","arguments":{"path":"a"}}`),
		},
		{
			Type: agent.EventTypeToolResult,
			Data: json.RawMessage(
				`{"callId":"call","name":"readFile","content":"bad","isError":true}`,
			),
		},
		{
			Type: frameTypeContentBlockDelta,
			Data: json.RawMessage(`{"delta":{"type":"text_delta","text":"done"}}`),
		},
	}
	for _, frame := range frames {
		require.NoError(t, output.render(frame))
	}
	require.NoError(t, output.finish())
	require.NoError(t, output.finish())
	assert.Equal(t, "reply\ndone\n", stdout.String())
	assert.Equal(t, "think\n> readFile {\"path\":\"a\"}\n"+
		"! readFile failed: bad\n", stderr.String())
}

func TestSummarize(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty"},
		{name: "one line", input: "one", want: "one"},
		{name: "newline", input: "one\ntwo", want: "one..."},
		{
			name: "exact limit", input: strings.Repeat("x", textSummaryLimit),
			want: strings.Repeat("x", textSummaryLimit),
		},
		{
			name: "truncated", input: strings.Repeat("x", textSummaryLimit+1),
			want: strings.Repeat("x", textSummaryLimit) + textEllipsis,
		},
		{
			name: "rune boundary", input: strings.Repeat("\u754c", textSummaryLimit),
			want: strings.Repeat("\u754c", textSummaryLimit/utf8.RuneLen('\u754c')) +
				textEllipsis,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := summarize(tc.input)
			assert.Equal(t, tc.want, got)
			assert.True(t, utf8.ValidString(got))
		})
	}
}

func TestTextRendererEventLines(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		typ  string
		data string
		want string
	}{
		{
			name: "child starts", typ: agent.EventTypeAgentRunStarted,
			data: `{"agentRunId":"child","event":{"name":"helper","model":"model"}}`,
			want: "agent child started: helper on model\n",
		},
		{
			name: "child completes", typ: agent.EventTypeAgentRunCompleted,
			data: `{"agentRunId":"child"}`, want: "agent child completed\n",
		},
		{
			name: "child fails", typ: agent.EventTypeAgentRunFailed,
			data: `{"agentRunId":"child"}`, want: "agent child failed\n",
		},
		{
			name: "child cancelled", typ: agent.EventTypeAgentRunCancelled,
			data: `{"agentRunId":"child"}`, want: "agent child cancelled\n",
		},
		{
			name: "turn fails", typ: agent.EventTypeTurnFailed,
			data: `{"reason":"agentRun"}`, want: "turn failed: agentRun\n",
		},
		{
			name: "turn cancelled", typ: agent.EventTypeTurnCancelled,
			data: `{}`, want: "turn cancelled\n",
		},
		{
			name: "message fails", typ: frameTypeMessageFailed,
			data: `{"code":"TURN_CANCELLED","message":"cancelled"}`,
			want: "message failed: TURN_CANCELLED: cancelled\n",
		},
		{
			name: "question", typ: agent.EventTypeQuestionAsked,
			data: `{"question":"continue?"}`, want: "? continue?\n",
		},
		{
			name: "successful tool is quiet", typ: agent.EventTypeToolResult,
			data: `{"callId":"call","name":"readFile","content":"ok","isError":false}`,
		},
		{name: "unknown frame is quiet", typ: "custom", data: `{}`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			output := newRenderer(OutputFormatText, stdout, stderr)
			require.NoError(t, output.render(controlclient.TurnFrame{
				Type: tc.typ, Data: json.RawMessage(tc.data),
			}))
			require.NoError(t, output.finish())
			assert.Equal(t, tc.want, stderr.String())
			assert.Empty(t, stdout.String())
		})
	}
}

func TestTextRendererKeepsExistingNewlines(t *testing.T) {
	t.Parallel()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	output := newRenderer(OutputFormatText, stdout, stderr)
	require.NoError(t, output.render(controlclient.TurnFrame{
		Type: frameTypeContentBlockDelta,
		Data: json.RawMessage(`{"delta":{"type":"text_delta","text":"reply\n"}}`),
	}))
	require.NoError(t, output.render(controlclient.TurnFrame{
		Type: agent.EventTypeTurnCancelled, Data: json.RawMessage(`{}`),
	}))
	require.NoError(t, output.finish())
	assert.Equal(t, "reply\n", stdout.String())
	assert.Equal(t, "turn cancelled\n", stderr.String())
}

func TestParseOutputFormat(t *testing.T) {
	t.Parallel()

	for _, format := range []OutputFormat{OutputFormatJSONL, OutputFormatText} {
		got, err := ParseOutputFormat(string(format))
		require.NoError(t, err)
		assert.Equal(t, format, got)
	}
	for _, invalid := range []string{"", "JSONL", "invalid"} {
		_, err := ParseOutputFormat(invalid)
		require.ErrorIs(t, err, commerr.ErrInvalidValue)
	}
}
