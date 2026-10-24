package agent

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderReasoningRoundTripsThroughStoredMessages(t *testing.T) {
	t.Parallel()

	const envelope = `{"reasoning_content":"earlier thought"}`

	testCases := []struct {
		name      string
		reasoning json.RawMessage
		want      string
	}{
		{name: "envelope", reasoning: json.RawMessage(envelope), want: envelope},
		{name: "absent"},
		{name: "null literal", reasoning: json.RawMessage("null")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			input, err := messageInput(elelem.Message{
				Role:              elelem.RoleAssistant,
				Content:           elelem.Text("answer"),
				ProviderReasoning: tc.reasoning,
			}, "provider/model")
			require.NoError(t, err)
			assert.Equal(t, tc.want, input.ProviderReasoningJSON)

			active, err := elelemMessageInput(input)
			require.NoError(t, err)

			replayed, err := elelemMessage(&models.Message{
				Role:                  input.Role,
				Content:               input.Content,
				ToolCallsJSON:         input.ToolCallsJSON,
				ProviderReasoningJSON: input.ProviderReasoningJSON,
			})
			require.NoError(t, err)

			for _, message := range []elelem.Message{active, replayed} {
				if tc.want == "" {
					assert.Nil(t, message.ProviderReasoning)

					continue
				}

				assert.JSONEq(t, tc.want, string(message.ProviderReasoning))
			}
		})
	}
}
