package agent

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errTestProviderUnavailable = errors.New("test provider unavailable")

const (
	retryProbeModelID = "retry-probe"

	// registryTestMaxContextTokens is small enough to fit inside any real
	// published window, so a case that does not set out to exceed one never
	// does.
	registryTestMaxContextTokens = 8192

	// registryTestKnownAnthropicModel is a model id the Anthropic driver
	// publishes a context window for, which is what makes the budget check
	// reachable at all.
	registryTestKnownAnthropicModel = "claude-sonnet-5"
	// registryTestAnthropicContextSize is that published window.
	registryTestAnthropicContextSize = 200_000

	// registryInvalidBudget is negative rather than zero, because the table
	// runner reads zero as "this case does not care" and substitutes the
	// default.
	registryInvalidBudget = -1

	// registryUnknownModelID is an id no driver catalog covers, which is what
	// a gateway or a local model looks like to discovery.
	registryUnknownModelID = "gateway/unlisted-model"
)

func TestNewRegistry(t *testing.T) {
	testCases := []struct {
		name             string
		upstreams        []config.Upstream
		defaultModel     string
		compactionModel  string
		maxContextTokens int
		drivers          map[string]elelem.Driver
		failProvider     string
		wantErr          error
		wantModels       []string
	}{
		{
			name: "discovers models and preserves model slashes",
			upstreams: []config.Upstream{
				{Name: "aigate", Provider: config.ProviderTypeOpenAI},
			},
			defaultModel: "aigate/gateway/model",
			drivers: map[string]elelem.Driver{
				"aigate": elelemtest.NewScriptedDriver().WithModels("gateway/model", "other"),
			},
			wantModels: []string{"aigate/gateway/model", "aigate/other"},
		},
		{
			name: "unselected provider failure does not block startup",
			upstreams: []config.Upstream{
				{Name: "aigate", Provider: config.ProviderTypeOpenAI},
				{Name: "offline", Provider: config.ProviderTypeOpenAI},
			},
			defaultModel: "aigate/model",
			drivers: map[string]elelem.Driver{
				"aigate": elelemtest.NewScriptedDriver().WithModels("model"),
			},
			failProvider: "offline",
			wantModels:   []string{"aigate/model"},
		},
		{
			name: "selected default model is unavailable",
			upstreams: []config.Upstream{
				{Name: "aigate", Provider: config.ProviderTypeOpenAI},
			},
			defaultModel: "aigate/missing",
			drivers: map[string]elelem.Driver{
				"aigate": elelemtest.NewScriptedDriver().WithModels("model"),
			},
			wantErr: ErrModelUnavailable,
		},
		{
			name: "selected compaction model is unavailable",
			upstreams: []config.Upstream{
				{Name: "aigate", Provider: config.ProviderTypeOpenAI},
			},
			defaultModel:    "aigate/default",
			compactionModel: "aigate/missing",
			drivers: map[string]elelem.Driver{
				"aigate": elelemtest.NewScriptedDriver().WithModels("default"),
			},
			wantErr: ErrModelUnavailable,
		},
		{
			name: "a budget above the model's published window",
			upstreams: []config.Upstream{
				{Name: "claude", Provider: config.ProviderTypeAnthropic},
			},
			defaultModel: "claude/" + registryTestKnownAnthropicModel,
			maxContextTokens: registryTestAnthropicContextSize +
				registryTestMaxContextTokens,
			drivers: map[string]elelem.Driver{
				"claude": elelemtest.NewScriptedDriver().
					WithModels(registryTestKnownAnthropicModel),
			},
			wantErr: ErrContextBudgetTooLarge,
		},
		{
			name: "a budget within the model's published window",
			upstreams: []config.Upstream{
				{Name: "claude", Provider: config.ProviderTypeAnthropic},
			},
			defaultModel:     "claude/" + registryTestKnownAnthropicModel,
			maxContextTokens: registryTestAnthropicContextSize,
			drivers: map[string]elelem.Driver{
				"claude": elelemtest.NewScriptedDriver().
					WithModels(registryTestKnownAnthropicModel),
			},
			wantModels: []string{
				"claude/" + registryTestKnownAnthropicModel,
			},
		},
		{
			name: "a compaction model above the published window",
			upstreams: []config.Upstream{
				{Name: "claude", Provider: config.ProviderTypeAnthropic},
			},
			defaultModel:    "claude/gateway-passthrough",
			compactionModel: "claude/" + registryTestKnownAnthropicModel,
			maxContextTokens: registryTestAnthropicContextSize +
				registryTestMaxContextTokens,
			drivers: map[string]elelem.Driver{
				"claude": elelemtest.NewScriptedDriver().WithModels(
					"gateway-passthrough",
					registryTestKnownAnthropicModel,
				),
			},
			wantErr: ErrContextBudgetTooLarge,
		},
		{
			name: "a non-positive budget is rejected",
			upstreams: []config.Upstream{
				{Name: "aigate", Provider: config.ProviderTypeOpenAI},
			},
			defaultModel:     "aigate/model",
			maxContextTokens: registryInvalidBudget,
			drivers: map[string]elelem.Driver{
				"aigate": elelemtest.NewScriptedDriver().WithModels("model"),
			},
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			maxContextTokens := tc.maxContextTokens
			if maxContextTokens == 0 {
				maxContextTokens = registryTestMaxContextTokens
			}

			registry, err := NewRegistry(
				context.Background(),
				RegistryOptions{
					Upstreams:        tc.upstreams,
					DefaultModel:     tc.defaultModel,
					CompactionModel:  tc.compactionModel,
					MaxContextTokens: maxContextTokens,
					Factory: testDriverFactory(
						tc.drivers,
						tc.failProvider,
					),
				},
			)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, registry)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, registry)
			assert.Equal(t, tc.wantModels, registry.modelReferences())

			for _, modelReference := range tc.wantModels {
				model, resolveErr := registry.ResolveModel(modelReference)
				require.NoError(t, resolveErr)
				assert.NotNil(t, model.Client)
				assert.NotEmpty(t, model.Model.ID)
			}
		})
	}
}

// TestNewRegistryModelContextSize covers the two halves of the context-size
// contract: a driver that publishes a window keeps it, and one that does not
// takes the deployment's budget as its size. Without the second half every
// discovered model reports a zero window, which disables Elelem's own
// size-dependent checks instead of bounding them.
func TestNewRegistryModelContextSize(t *testing.T) {
	testCases := []struct {
		name     string
		provider config.ProviderType
		modelID  string
		want     int
	}{
		{
			name:     "a published window is preserved",
			provider: config.ProviderTypeAnthropic,
			modelID:  registryTestKnownAnthropicModel,
			want:     registryTestAnthropicContextSize,
		},
		{
			name:     "an unlisted model takes the configured budget",
			provider: config.ProviderTypeAnthropic,
			modelID:  registryUnknownModelID,
			want:     registryTestMaxContextTokens,
		},
		{
			name:     "an unlisted OpenAI model takes the configured budget",
			provider: config.ProviderTypeOpenAI,
			modelID:  registryUnknownModelID,
			want:     registryTestMaxContextTokens,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reference := "provider/" + tc.modelID
			registry, err := NewRegistry(
				context.Background(),
				RegistryOptions{
					Upstreams: []config.Upstream{
						{Name: "provider", Provider: tc.provider},
					},
					DefaultModel:     reference,
					MaxContextTokens: registryTestMaxContextTokens,
					Factory: testDriverFactory(
						map[string]elelem.Driver{
							"provider": elelemtest.NewScriptedDriver().
								WithModels(tc.modelID),
						},
						"",
					),
				},
			)
			require.NoError(t, err)

			model, err := registry.ResolveModel(reference)
			require.NoError(t, err)
			assert.Equal(t, tc.want, model.Model.ContextSize)
			assert.Equal(t, tc.modelID, model.Model.ID)
		})
	}
}

func TestNewStaticRegistry(t *testing.T) {
	client := elelem.New(elelemtest.NewScriptedDriver())

	testCases := []struct {
		name    string
		models  map[string]ModelClient
		wantErr error
	}{
		{
			name: "valid static model",
			models: map[string]ModelClient{
				"test/mock-model": {Client: client, Model: elelem.Model{ID: "mock-model"}},
			},
		},
		{
			name: "malformed qualified name",
			models: map[string]ModelClient{
				"missing-separator": {Client: client, Model: elelem.Model{ID: "mock-model"}},
			},
			wantErr: ErrInvalidModelReference,
		},
		{
			name: "missing client",
			models: map[string]ModelClient{
				"test/mock-model": {Model: elelem.Model{ID: "mock-model"}},
			},
			wantErr: ErrModelUnavailable,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			registry, err := NewStaticRegistry(tc.models)
			if tc.wantErr == nil {
				require.NoError(t, err)
				require.NotNil(t, registry)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, registry)
		})
	}
}

func TestRegistryRejectsMalformedReference(t *testing.T) {
	registry, err := NewStaticRegistry(map[string]ModelClient{
		"test/mock-model": {
			Client: elelem.New(elelemtest.NewScriptedDriver()),
			Model:  elelem.Model{ID: "mock-model"},
		},
	})
	require.NoError(t, err)

	_, err = registry.ResolveModel("malformed")
	require.ErrorIs(t, err, ErrInvalidModelReference)
}

func TestProviderRetryPolicy(t *testing.T) {
	testCases := []struct {
		name              string
		errors            []error
		wantCalls         int
		wantTotalAttempts int
		wantErr           bool
	}{
		{
			name: "retries transient provider errors ten times",
			errors: []error{&elelem.ProviderError{
				Cause:      errTestProviderUnavailable,
				StatusCode: http.StatusServiceUnavailable,
			}},
			wantCalls:         providerMaximumAttempts,
			wantTotalAttempts: providerMaximumAttempts,
			wantErr:           true,
		},
		{
			name: "does not retry permanent provider errors",
			errors: []error{&elelem.ProviderError{
				Cause:      errTestProviderUnavailable,
				StatusCode: http.StatusBadRequest,
			}},
			wantCalls:         1,
			wantTotalAttempts: 1,
			wantErr:           true,
		},
		{
			name: "returns after a retried call succeeds",
			errors: []error{
				&elelem.ProviderError{
					Cause:      errTestProviderUnavailable,
					StatusCode: http.StatusTooManyRequests,
				},
				nil,
			},
			wantCalls:         2,
			wantTotalAttempts: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			driver := &retryProbeDriver{errors: tc.errors}
			retryConfig := providerRetryConfig()
			retryConfig.InitialDelay = time.Nanosecond
			retryConfig.MaxDelay = time.Nanosecond
			retryConfig.Jitter = new(false)
			client := elelem.New(withRetryConfig(driver, retryConfig))

			response, err := elelem.NewRequest(client).
				WithModel(elelem.Model{ID: retryProbeModelID}).
				WithPrompt(elelem.NewPrompt().UserText("retry probe")).
				Run(context.Background())
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.NotNil(t, response)
			assert.Equal(t, tc.wantCalls, driver.calls)
			assert.Equal(
				t,
				tc.wantTotalAttempts,
				response.Usage.Retry.TotalAttempts,
			)
		})
	}
}

func testDriverFactory(
	drivers map[string]elelem.Driver,
	failProvider string,
) DriverFactory {
	return func(upstream config.Upstream) (elelem.Driver, error) {
		if upstream.Name == failProvider {
			return nil, errTestProviderUnavailable
		}

		return drivers[upstream.Name], nil
	}
}

func (r *Registry) modelReferences() []string {
	return slices.Sorted(maps.Keys(r.models))
}

type retryProbeDriver struct {
	errors []error
	calls  int
}

func (d *retryProbeDriver) Stream(
	_ context.Context,
	_ elelem.DriverRequest,
	_ func(elelem.Delta) error,
) (elelem.Usage, error) {
	d.calls++

	return elelem.Usage{
		FinishReason: elelem.FinishReasonStop,
	}, d.nextError()
}

func (d *retryProbeDriver) Complete(
	ctx context.Context,
	request elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	return d.Stream(ctx, request, onDelta)
}

func (d *retryProbeDriver) ListModels(context.Context) ([]string, error) {
	return []string{retryProbeModelID}, nil
}

func (d *retryProbeDriver) Capabilities(elelem.Model) elelem.Capabilities {
	return elelem.Capabilities{}
}

func (d *retryProbeDriver) TokenCounter() elelem.TokenCounter { //nolint:ireturn // satisfies elelem.Driver's interface-returning method signature
	return elelem.DefaultTokenCounter()
}

func (d *retryProbeDriver) nextError() error {
	index := d.calls - 1
	if index >= len(d.errors) {
		return d.errors[len(d.errors)-1]
	}

	return d.errors[index]
}

var _ elelem.Driver = (*retryProbeDriver)(nil)
