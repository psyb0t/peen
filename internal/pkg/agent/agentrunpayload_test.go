package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewAgentRunAssistantMessagePayloadJSONShape pins the Peen-owned
// camelCase encoding of a child's assistant message: no vendored elelem
// struct, content and toolCalls always arrays, and the omitempty fields
// dropped when elelem left them unset.
func TestNewAgentRunAssistantMessagePayloadJSONShape(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		message elelem.Message
		want    string
	}{
		{
			name:    "minimal",
			message: elelem.Message{Role: elelem.RoleAssistant},
			want:    `{"role":"assistant","content":[],"toolCalls":[]}`,
		},
		{
			name: "full",
			message: elelem.Message{
				Role:              elelem.RoleAssistant,
				Content:           elelem.Text("hi there"),
				ToolCalls:         []elelem.ToolCall{{ID: "call-1", Name: "readFile", Arguments: json.RawMessage(`{"path":"/tmp"}`)}},
				Reasoning:         "thinking it through",
				ProviderReasoning: json.RawMessage(`{"encrypted":"abc"}`),
				Origin:            elelem.MessageOriginTurn,
			},
			want: `{
				"role": "assistant",
				"content": [{"type": "text", "text": "hi there"}],
				"toolCalls": [
					{"id": "call-1", "name": "readFile", "arguments": {"path": "/tmp"}}
				],
				"reasoning": "thinking it through",
				"providerReasoning": {"encrypted": "abc"},
				"origin": "turn"
			}`,
		},
		{
			name: "provider reasoning null is dropped",
			message: elelem.Message{
				Role:              elelem.RoleAssistant,
				ProviderReasoning: json.RawMessage(`null`),
			},
			want: `{"role":"assistant","content":[],"toolCalls":[]}`,
		},
		{
			name: "irrelevant tool-message and injection fields never appear",
			message: elelem.Message{
				Role:              elelem.RoleAssistant,
				ToolCallID:        "should-not-appear",
				ToolResultIsError: true,
				CacheHint:         elelem.CacheHintLong,
			},
			want: `{"role":"assistant","content":[],"toolCalls":[]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := json.Marshal(
				newAgentRunAssistantMessagePayload(tc.message),
			)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(encoded))
		})
	}
}

// TestNewAgentRunAssistantMessagePayloadPreservesArgumentOrder confirms a
// tool call's arguments pass through as elelem received them, key order
// included, rather than through a decode/re-encode that could reorder an
// object's keys or normalize a literal null.
func TestNewAgentRunAssistantMessagePayloadPreservesArgumentOrder(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		arguments json.RawMessage
		wantToken string
	}{
		{
			name:      "object with non-alphabetical keys",
			arguments: json.RawMessage(`{"zeta":1,"alpha":2}`),
			wantToken: `"arguments":{"zeta":1,"alpha":2}`,
		},
		{
			name:      "null",
			arguments: json.RawMessage(`null`),
			wantToken: `"arguments":null`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := newAgentRunAssistantMessagePayload(elelem.Message{
				Role: elelem.RoleAssistant,
				ToolCalls: []elelem.ToolCall{
					{ID: "call-1", Name: "tool", Arguments: tc.arguments},
				},
			})

			encoded, err := json.Marshal(payload)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), tc.wantToken)
		})
	}
}

// TestAgentRunSinkOnAssistantMessageEmitsPeenPayload proves the emitted
// event the sink persists and streams is the Peen-owned payload, not a
// marshal of the vendored elelem.Message.
func TestAgentRunSinkOnAssistantMessageEmitsPeenPayload(t *testing.T) {
	t.Parallel()

	driver := elelemtest.NewScriptedDriver()
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})
	registry, run, ctx, sink := startDurableAgentRun(
		context.Background(), t, fixture,
	)
	t.Cleanup(func() {
		fixture.runtime.finishAgentRun(
			context.Background(), registry, sink, run, nil,
			&wrappedCancelError{},
		)
	})

	message := elelem.Message{
		Role:    elelem.RoleAssistant,
		Content: elelem.Text("done"),
		ToolCalls: []elelem.ToolCall{
			{ID: "call-1", Name: "readFile", Arguments: json.RawMessage(`{}`)},
		},
	}
	require.NoError(t, sink.onAssistantMessage(ctx, message))

	page, err := fixture.store.ListAgentRunEvents(ctx, run.SessionID,
		run.ID, session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, EventTypeAgentRunAssistantMessage, page.Items[0].EventType)
	assert.JSONEq(t,
		`{"role":"assistant","content":[{"type":"text","text":"done"}],`+
			`"toolCalls":[{"id":"call-1","name":"readFile","arguments":{}}]}`,
		page.Items[0].PayloadJSON,
	)
	assert.NotContains(t, page.Items[0].PayloadJSON, `"Role"`)
	assert.NotContains(t, page.Items[0].PayloadJSON, `"ToolCalls"`)
}

// TestAgentRunSinkOnMessageInjectionEmitsPeenPayload proves the emitted
// event for a child tool injection is the Peen-owned camelCase payload,
// not a marshal of the vendored elelem.MessageInjection.
func TestAgentRunSinkOnMessageInjectionEmitsPeenPayload(t *testing.T) {
	t.Parallel()

	driver := elelemtest.NewScriptedDriver()
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})
	registry, run, ctx, sink := startDurableAgentRun(
		context.Background(), t, fixture,
	)
	t.Cleanup(func() {
		fixture.runtime.finishAgentRun(
			context.Background(), registry, sink, run, nil,
			&wrappedCancelError{},
		)
	})

	injection := elelem.MessageInjection{
		Type:    elelem.RoleUser,
		Content: "keep going",
		Phase:   elelem.ToolPhaseOnSuccess,
		Tool:    "readFile",
		CallID:  "call-1",
		Round:   2,
	}
	require.NoError(t, sink.onMessageInjection(ctx, injection))

	page, err := fixture.store.ListAgentRunEvents(ctx, run.SessionID,
		run.ID, session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, EventTypeAgentRunMessageInjected, page.Items[0].EventType)
	assert.JSONEq(t,
		`{"type":"user","content":"keep going","phase":"on_success",`+
			`"tool":"readFile","callId":"call-1","round":2}`,
		page.Items[0].PayloadJSON,
	)
	assert.NotContains(t, page.Items[0].PayloadJSON, `"Type"`)
	assert.NotContains(t, page.Items[0].PayloadJSON, `"CallID"`)
}
