package agent

import (
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	budgetTestDefaultModel    = "provider/large"
	budgetTestCompactionModel = "provider/small"
	budgetTestLargeWindow     = 1_000_000
	budgetTestSmallWindow     = 200_000
	budgetTestMaxOutput       = 131_072
	// budgetTestSmallReserve is a quarter of budgetTestSmallWindow, the most
	// an automatic output limit takes of that context.
	budgetTestSmallReserve   = 50_000
	budgetTestSummaryCeiling = 1024
)

func TestResolveContextBudget(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		compactionModel string
		wantContext     int
		wantReserve     int
	}{
		{
			name:        "the default model alone sets the budget",
			wantContext: budgetTestLargeWindow, wantReserve: budgetTestMaxOutput,
		},
		{
			name:            "a smaller compaction window bounds the context",
			compactionModel: budgetTestCompactionModel,
			wantContext:     budgetTestSmallWindow,
			wantReserve:     budgetTestSmallReserve,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deployment := config.Config{
				DefaultModel:           budgetTestDefaultModel,
				CompactionModel:        tc.compactionModel,
				CompactionOutputTokens: budgetTestSummaryCeiling,
			}

			resolved, err := ResolveContextBudget(deployment, budgetTestModels(t))
			require.NoError(t, err)
			assert.Equal(t, tc.wantContext, resolved.MaxContextTokens)
			assert.Equal(t, tc.wantReserve, resolved.OutputReserveTokens)
		})
	}
}

func TestResolveContextBudgetRefusesAnUnknownModel(t *testing.T) {
	t.Parallel()
	deployment := config.Config{
		DefaultModel:           "provider/missing",
		CompactionOutputTokens: budgetTestSummaryCeiling,
	}

	_, err := ResolveContextBudget(deployment, budgetTestModels(t))
	require.ErrorIs(t, err, ErrModelUnavailable)
}

// An unlisted model with no configured budget takes the fallback context
// rather than 0, which would turn off elelem's own size checks.
func TestNewRegistryWithoutABudgetUsesTheFallbackContext(t *testing.T) {
	t.Parallel()
	reference := "provider/" + registryUnknownModelID
	registry, err := NewRegistry(t.Context(), RegistryOptions{
		Upstreams:    []config.Upstream{{Name: "provider", Type: config.UpstreamTypeOpenAI}},
		DefaultModel: reference,
		Factory: testDriverFactory(map[string]elelem.Driver{
			"provider": elelemtest.NewScriptedDriver().
				WithModels(registryUnknownModelID),
		}, ""),
	})
	require.NoError(t, err)

	model, err := registry.ResolveModel(reference)
	require.NoError(t, err)
	assert.Equal(t, config.FallbackContextTokens, model.Model.ContextSize)
}

func budgetTestModels(t *testing.T) *Registry {
	t.Helper()
	client := elelem.New(elelemtest.NewScriptedDriver())
	registry, err := NewStaticRegistry(map[string]ModelClient{
		budgetTestDefaultModel: {Client: client, Model: elelem.Model{
			ID: "large", ContextSize: budgetTestLargeWindow,
			MaxOutputTokens: budgetTestMaxOutput,
		}},
		budgetTestCompactionModel: {Client: client, Model: elelem.Model{
			ID: "small", ContextSize: budgetTestSmallWindow,
		}},
	})
	require.NoError(t, err)

	return registry
}
