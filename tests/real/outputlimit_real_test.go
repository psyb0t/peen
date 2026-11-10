//go:build real

package realtest

import (
	"context"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// realOutputLimitTokens is far below what the reasoning prompt makes a
	// thinking model write, so only a limit the provider enforces keeps the
	// reply under it.
	realOutputLimitTokens = 512

	realOutputLimitPrompt = "Work through this carefully, checking every " +
		"step before you answer. How many positive integers below 10000 " +
		"have digits that sum to 23 and are divisible by 7? Show the " +
		"count only."
)

// TestRealProviderOutputLimitCoversReasoning sends the default model a prompt
// that needs long reasoning, at its highest reasoning level, with a small
// output limit. The provider must stop the reply at the limit, reasoning
// included, so a request's output never grows past the room kept for it.
func TestRealProviderOutputLimitCoversReasoning(t *testing.T) {
	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), realProviderTimeout)
	t.Cleanup(cancel)

	registry, err := agent.NewRegistry(ctx, agent.RegistryOptions{
		Upstreams:        upstreams,
		DefaultModel:     configured.DefaultModel,
		CompactionModel:  configured.CompactionModel,
		MaxContextTokens: configured.MaxContextTokens,
	})
	require.NoError(t, err)

	selected, err := registry.ResolveModel(configured.DefaultModel)
	require.NoError(t, err)

	model := selected.Model
	require.True(t, model.IsReasoning(), "model %q does not reason",
		configured.DefaultModel)

	response, err := elelem.NewRequest(selected.Client).
		WithModel(model).
		WithPrompt(elelem.NewPrompt().UserText(realOutputLimitPrompt)).
		WithReasoningEffort(model.ReasoningLevelMax()).
		WithMaxOutputTokens(realOutputLimitTokens).
		Run(ctx)
	require.NoError(t, err)

	usage := response.Usage
	t.Logf("finish %q, completion %d, reasoning %d, reasoning text %d "+
		"bytes, answer %d bytes", response.FinishReason, usage.Completion,
		usage.Reasoning, len(response.Reasoning), len(response.Text))

	assert.LessOrEqual(t, usage.Completion, int64(realOutputLimitTokens))
	assert.Equal(t, elelem.FinishReasonLength, response.FinishReason)
	assert.NotEmpty(t, response.Reasoning)
}
