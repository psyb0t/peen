package hooks

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A zero UUID is a [16]byte, which omitempty never drops, so a hook script
// would read an all-zero ID for a turn that has none.
func TestInvocationJSONOmitsZeroIdentifiers(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		invocation Invocation
		wantKeys   []string
		absentKeys []string
	}{
		{
			name:       "no identifiers",
			invocation: Invocation{Event: harness.HookEventPreUserMessage},
			absentKeys: []string{"sessionId", "requestId", "turnId"},
		},
		{
			name: "session and request without a turn",
			invocation: Invocation{
				Event:     harness.HookEventPreUserMessage,
				SessionID: uuid.New(),
				RequestID: uuid.New(),
			},
			wantKeys:   []string{"sessionId", "requestId"},
			absentKeys: []string{"turnId"},
		},
		{
			name: "all identifiers",
			invocation: Invocation{
				Event:     harness.HookEventPreUserMessage,
				SessionID: uuid.New(),
				RequestID: uuid.New(),
				TurnID:    uuid.New(),
			},
			wantKeys: []string{"sessionId", "requestId", "turnId"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(tc.invocation)
			require.NoError(t, err)

			var decoded map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &decoded))

			for _, key := range tc.wantKeys {
				assert.Contains(t, decoded, key)
			}

			for _, key := range tc.absentKeys {
				assert.NotContains(t, decoded, key)
			}

			assert.Contains(t, decoded, "contextTokens")
		})
	}
}
