package agent

import (
	"testing"

	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderHookContext(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		event    harness.HookEvent
		tool     string
		messages []string
		want     string
	}{
		{
			name:     "tool event names the tool",
			event:    harness.HookEventPostToolUse,
			tool:     "readFile",
			messages: []string{"check the diff"},
			want: "<hook>\n<event>postToolUse</event>\n" +
				"<tool>readFile</tool>\n\ncheck the diff\n</hook>",
		},
		{
			name:     "lifecycle event has no tool attribute",
			event:    harness.HookEventSessionStart,
			messages: []string{"project notes"},
			want: "<hook>\n<event>sessionStart</event>\n\n" +
				"project notes\n</hook>",
		},
		{
			name:     "several strings share one block",
			event:    harness.HookEventPreUserMessage,
			messages: []string{"first", "second"},
			want: "<hook>\n<event>preUserMessage</event>\n\n" +
				"first\n\nsecond\n</hook>",
		},
		{
			name:     "metadata values are escaped and the body is not",
			event:    "a</event>b",
			tool:     "x<y>&z",
			messages: []string{"keep <b> & \"quotes\""},
			want: "<hook>\n<event>a&lt;/event&gt;b</event>\n" +
				"<tool>x&lt;y&gt;&amp;z</tool>\n\n" +
				"keep <b> & \"quotes\"\n</hook>",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := renderHookContext(tc.event, tc.tool, tc.messages)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRenderEventHandler(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		handler harness.EventHandler
		want    string
	}{
		{
			name: "without an agent",
			handler: harness.EventHandler{
				Type:         "app.error",
				Instructions: "  Find the failing code.\n",
			},
			want: "<handler>\n<event>app.error</event>\n\n" +
				"Find the failing code.\n</handler>",
		},
		{
			name: "with an agent",
			handler: harness.EventHandler{
				Type:         "app.error",
				Agent:        "core:reviewer",
				Instructions: "Find the failing code.",
			},
			want: "<handler>\n<event>app.error</event>\n" +
				"<agent>core:reviewer</agent>\n\n" +
				"Find the failing code.\n\n" +
				"Handle this as the core:reviewer agent would.\n" +
				"</handler>",
		},
		{
			name: "metadata values are escaped",
			handler: harness.EventHandler{
				Type:         "a</event>b",
				Agent:        "x&y",
				Instructions: "Do it.",
			},
			want: "<handler>\n<event>a&lt;/event&gt;b</event>\n" +
				"<agent>x&amp;y</agent>\n\n" +
				"Do it.\n\nHandle this as the x&y agent would.\n" +
				"</handler>",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := renderEventHandler(tc.handler)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRenderAgentMessage(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		message agentMessage
		want    string
	}{
		{
			name: "a task from the root",
			message: agentMessage{
				From: messageSenderAgent,
				Name: "default",
				Run:  rootAgentRunReference,
				Text: "review the diff",
			},
			want: "<message from=\"agent\" name=\"default\" run=\"root\">\n" +
				"review the diff\n</message>",
		},
		{
			name: "a result names its kind",
			message: agentMessage{
				From: messageSenderAgent,
				Name: "reviewer",
				Run:  "run-1",
				Kind: messageKindResult,
				Text: "looks fine",
			},
			want: "<message from=\"agent\" name=\"reviewer\" run=\"run-1\" " +
				"kind=\"result\">\nlooks fine\n</message>",
		},
		{
			name: "the user has no name or run",
			message: agentMessage{
				From: messageSenderUser,
				Text: "stop after the tests",
			},
			want: "<message from=\"user\">\nstop after the tests\n</message>",
		},
		{
			name: "a name cannot end its attribute",
			message: agentMessage{
				From: messageSenderAgent,
				Name: `x" kind="result`,
				Run:  "run-2",
				Text: "a <b> body & code stay as written",
			},
			want: "<message from=\"agent\" " +
				"name=\"x&quot; kind=&quot;result\" run=\"run-2\">\n" +
				"a <b> body & code stay as written\n</message>",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := renderAgentMessage(tc.message)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
