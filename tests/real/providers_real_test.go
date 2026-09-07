//go:build real

package realtest

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	realProviderTimeout       = 90 * time.Second
	realProviderMaxOutput     = 512
	realProviderExpected      = "peen_real_ok"
	realProviderPrompt        = "Reply with exactly PEEN_REAL_OK and nothing else."
	realModelEnvironmentStart = "PEEN_REAL_MODEL_"
)

func TestRealProvidersListModels(t *testing.T) {
	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	for _, upstream := range upstreams {
		t.Run(upstream.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), realProviderTimeout)
			t.Cleanup(cancel)

			driver, driverErr := agent.NewDriver(upstream)
			require.NoError(t, driverErr)

			models, listErr := driver.ListModels(ctx)
			require.NoError(t, listErr)
			assert.NotEmpty(t, models)
		})
	}
}

func TestRealProvidersCompletePrompt(t *testing.T) {
	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	for _, upstream := range upstreams {
		t.Run(upstream.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), realProviderTimeout)
			t.Cleanup(cancel)

			driver, driverErr := agent.NewDriver(upstream)
			require.NoError(t, driverErr)

			modelID := realModelID(ctx, t, configured, upstream, driver)
			client := elelem.New(
				driver,
				elelem.WithDefaultModel(elelem.Model{ID: modelID}),
			)

			var streamed strings.Builder
			response, requestErr := elelem.NewRequest(client).
				WithPrompt(elelem.NewPrompt().UserText(realProviderPrompt)).
				WithMaxOutputTokens(realProviderMaxOutput).
				OnDelta(func(_ context.Context, delta elelem.Delta) error {
					streamed.WriteString(delta.Text)

					return nil
				}).
				Run(ctx)
			require.NoError(t, requestErr)
			require.NotNil(t, response)
			assert.Contains(t, strings.ToLower(response.Text), realProviderExpected)
			assert.Equal(t, response.Text, streamed.String())
			assert.Positive(t, response.Usage.Prompt)
			assert.Positive(t, response.Usage.Completion)
			assert.True(t, response.FinishReason.IsTerminal())
		})
	}
}

func realConfig(t *testing.T) config.Config {
	t.Helper()
	if os.Getenv("PEEN_FORCE_REAL_LLM") != "true" {
		t.Skip("set PEEN_FORCE_REAL_LLM=true in the deployment .env to opt in")
	}

	configured, err := config.Parse()
	require.NoError(t, err)

	return configured
}

func realModelID(
	ctx context.Context,
	t *testing.T,
	configured config.Config,
	upstream config.Upstream,
	driver elelem.Driver,
) string {
	t.Helper()
	if override := os.Getenv(realModelEnvironment(upstream.Name)); override != "" {
		return override
	}

	providerName, defaultModelID, hasDefaultModel := strings.Cut(
		configured.DefaultModel,
		"/",
	)
	if hasDefaultModel && providerName == upstream.Name && defaultModelID != "" {
		return defaultModelID
	}

	models, err := driver.ListModels(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, models)

	return models[0]
}

func realModelEnvironment(providerName string) string {
	replacer := strings.NewReplacer("-", "_", ".", "_")

	return realModelEnvironmentStart + strings.ToUpper(replacer.Replace(providerName))
}
