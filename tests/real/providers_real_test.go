//go:build real

package realtest

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	realProviderTimeout      = 90 * time.Second
	realProviderDefaultModel = "PEEN_TEST_DEFAULT_MODEL"
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

func realConfig(t *testing.T) config.Config {
	t.Helper()

	configured, err := config.Parse()
	require.NoError(t, err)

	if defaultModel := strings.TrimSpace(os.Getenv(realProviderDefaultModel)); defaultModel != "" {
		configured.DefaultModel = defaultModel
	}

	return configured
}

func upstreamByName(upstreams []config.Upstream, providerName string) (config.Upstream, bool) {
	for _, upstream := range upstreams {
		if upstream.Name == providerName {
			return upstream, true
		}
	}

	return config.Upstream{}, false
}
