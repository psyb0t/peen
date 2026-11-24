//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"path"
	"testing"

	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIStructuredOutput(t *testing.T) {
	for _, valid := range []bool{true, false} {
		name := "valid"
		if !valid {
			name = "exhausted"
		}

		t.Run(name, func(t *testing.T) {
			workspace := openFreshWorkspace(t, "structured-output-",
				map[string]string{"input.txt": "fixture"})
			connection := openSessionConnection(t, workspace.sessionID)
			if !valid {
				t.Cleanup(integrationInfra.DisableScriptedToolTurn)
				integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
					UserMessage: "produce a structured answer", FinalAnswer: "done",
					StructuredAnswer: "invalid JSON",
					Calls: []testinfra.ScriptedToolCall{{
						Name:      "readFile",
						Arguments: map[string]any{"path": path.Join(workspace.directory, "input.txt")},
					}},
				})
			}

			event := dabluveees.NewEvent(apiTestWebSocketMessageSend, map[string]any{
				"message": "produce a structured answer",
				"outputSchema": map[string]any{
					"type": "object", "required": []string{"answer"},
					"properties": map[string]any{"answer": map[string]any{"type": "integer"}},
				},
			}).SetMetadata(apiTestWebSocketSessionIDParameter, workspace.sessionID.String())
			assertValidMessageSend(t, event)
			require.NoError(t, connection.WriteJSON(event))
			completed := awaitAPIWebSocketEventOfType(t, connection, "turn.completed")
			var output wsapi.TurnCompletedData
			require.NoError(t, json.Unmarshal(completed.Data, &output))
			response := apiRequest(t, http.MethodGet, "/api/v1/session/turns", nil,
				withHeader(authenticatedHeaders(), headerSessionID, workspace.sessionID.String()))
			require.Equal(t, http.StatusOK, response.StatusCode)
			page := decodeResponse[api.TurnPage](t, response)
			require.Len(t, page.Turns, 1)
			if valid {
				assert.JSONEq(t, `{"answer":7}`, string(output.Output))
				stored, err := json.Marshal(page.Turns[0].Output)
				require.NoError(t, err)
				assert.JSONEq(t, `{"answer":7}`, string(stored))
				assert.Nil(t, output.OutputError)
			} else {
				assert.Empty(t, output.Output)
				require.NotNil(t, output.OutputError)
				assert.NotEmpty(t, *output.OutputError)
				assert.Equal(t, output.OutputError, page.Turns[0].OutputError)
			}
		})
	}
}

func TestAPIRejectsAnInvalidOutputSchema(t *testing.T) {
	workspace := openFreshWorkspace(t, "invalid-output-schema-", nil)
	connection := openSessionConnection(t, workspace.sessionID)
	event := dabluveees.NewEvent(apiTestWebSocketMessageSend,
		map[string]any{
			"message":      "produce an answer",
			"outputSchema": map[string]any{"type": "unknown"},
		}).SetMetadata(apiTestWebSocketSessionIDParameter,
		workspace.sessionID.String())
	assertValidMessageSend(t, event)
	require.NoError(t, connection.WriteJSON(event))
	result := awaitAPIWebSocketFailure(t, connection)
	assert.Equal(t, server.ErrorCodeOutputSchemaInvalid, result.Code)
}
