package wsapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmptyOutputSchemaSurvivesWireRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		schema map[string]any
	}{
		{name: "absent"},
		{name: "empty object", schema: map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(MessageSendData{
				Message: "request", OutputSchema: tc.schema,
			})
			require.NoError(t, err)
			var restored MessageSendData
			require.NoError(t, json.Unmarshal(encoded, &restored))
			assert.Equal(t, tc.schema, restored.OutputSchema)
		})
	}
}

func TestTurnSettingsFailureCodesSurviveWireRoundTrip(t *testing.T) {
	t.Parallel()
	for _, code := range []MessageFailedCode{
		MessageFailedCodeTurnTimeout, MessageFailedCodeOutputSchemaInvalid,
	} {
		t.Run(string(code), func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(MessageFailedData{
				Code: code, Message: "turn settings refused",
			})
			require.NoError(t, err)
			var decoded MessageFailedData
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			assert.Equal(t, code, decoded.Code)
		})
	}
}
