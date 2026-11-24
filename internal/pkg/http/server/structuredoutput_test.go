package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStructuredMessageAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		data    string
		maximum int
		code    string
	}{
		{name: "valid", data: `{"message":"task","outputSchema":{},"timeoutSeconds":30}`},
		{name: "schema with child", data: `{"message":"task","outputSchema":{},"to":"` + uuid.NewString() + `"}`, code: "VALIDATION_FAILED"},
		{name: "timeout with child", data: `{"message":"task","timeoutSeconds":1,"to":"` + uuid.NewString() + `"}`, code: "VALIDATION_FAILED"},
		{name: "remote", data: `{"message":"task","outputSchema":{"$ref":"https://schema.example/value"}}`, code: "OUTPUT_SCHEMA_INVALID"},
		{name: "oversize", data: `{"message":"task","outputSchema":{}}`, maximum: 1, code: "OUTPUT_SCHEMA_INVALID"},
		{name: "whitespace counts", data: `{"message":"task","outputSchema":{` + strings.Repeat(" ", 50) + `}}`, maximum: 20, code: "OUTPUT_SCHEMA_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request, err := decodeWebSocketMessageWithLimit(json.RawMessage(tc.data), tc.maximum)
			if tc.code != "" {
				require.Error(t, err)
				assert.Equal(t, tc.code, webSocketMessageFailureFor(err).Code)

				return
			}

			require.NoError(t, err)
			assert.JSONEq(t, `{}`, string(request.OutputSchema))
			assert.Equal(t, 30, request.TimeoutSeconds)
		})
	}

	assert.Equal(t, "TURN_TIMEOUT", webSocketMessageFailureFor(agent.ErrTurnTimeout).Code)
}
