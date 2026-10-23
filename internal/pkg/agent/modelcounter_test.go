package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/drivers/openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelTokenCounterCountsOnlyReplayedReasoning(t *testing.T) {
	t.Parallel()

	const savedModel = "example-model"

	driver := openai.NewDriver(openai.WithoutEnvironmentDefaults())
	client := elelem.New(driver)
	reasoning := strings.Repeat("saved reasoning ", 100)
	opaque, err := json.Marshal(map[string]any{
		"provider": openai.Name, "version": 1, "model": savedModel,
		"fields": map[string]any{"reasoning_content": reasoning},
	})
	require.NoError(t, err)

	testCases := []struct {
		name       string
		model      string
		opaque     json.RawMessage
		wantLarger bool
	}{
		{name: "display only", model: savedModel},
		{
			name: "same model", model: savedModel,
			opaque: opaque, wantLarger: true,
		},
		{name: "different model", model: "other-model", opaque: opaque},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			counter := modelTokenCounter(ModelClient{
				Client: client, Model: elelem.Model{ID: tc.model},
			})
			message := elelem.Message{
				Role: elelem.RoleAssistant, Content: elelem.Text("answer"),
			}
			baseline, err := counter.Count([]elelem.Message{message}, nil)
			require.NoError(t, err)

			message.Reasoning = reasoning
			message.ProviderReasoning = tc.opaque
			count, err := counter.Count([]elelem.Message{message}, nil)
			require.NoError(t, err)
			if tc.wantLarger {
				assert.Greater(t, count, baseline)

				return
			}

			assert.Equal(t, baseline, count)
		})
	}
}
