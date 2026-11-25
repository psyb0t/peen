package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A child row stores elelem's tool calls under Go field names. The API
// returns them with the same camelCase keys the session transcript uses.
func TestAgentRunMessageToAPIUsesTheTranscriptToolCallShape(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		toolCallsJSON string
		wantJSON      string
	}{
		{
			name:          "stored Go field names",
			toolCallsJSON: `[{"ID":"c1","Name":"ls","Arguments":{"p":"a"}}]`,
			wantJSON:      `[{"id":"c1","name":"ls","arguments":{"p":"a"}}]`,
		},
		{
			name:          "no tool calls",
			toolCallsJSON: `[]`,
			wantJSON:      `[]`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			converted, err := agentRunMessageToAPI(&models.AgentRunMessage{
				ID:            uuid.New(),
				SessionID:     uuid.New(),
				AgentRunID:    uuid.New(),
				Sequence:      1,
				Role:          models.MessageRoleAssistant,
				ToolCallsJSON: tc.toolCallsJSON,
				CreatedAt:     time.Now().UTC(),
			})
			require.NoError(t, err)
			require.NotNil(t, converted.ToolCalls)

			encoded, err := json.Marshal(converted)
			require.NoError(t, err)

			var decoded struct {
				ToolCalls json.RawMessage `json:"toolCalls"`
			}
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			assert.JSONEq(t, tc.wantJSON, string(decoded.ToolCalls))
		})
	}
}
