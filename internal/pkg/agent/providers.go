// Package agent owns Peen's transport-independent agent runtime.
package agent

import (
	"context"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/drivers/anthropic"
	"github.com/psyb0t/elelem/drivers/openai"
	"github.com/psyb0t/elelem/drivers/zaicoding"
	"github.com/psyb0t/peen/internal/pkg/config"
)

const (
	providerRetryCount        = 10
	providerInitialRetryDelay = 250 * time.Millisecond
	providerMaximumRetryDelay = 5 * time.Second

	providerMaximumAttempts = providerRetryCount + 1

	settingDefaultModel    = "PEEN_DEFAULT_MODEL"
	settingCompactionModel = "PEEN_COMPACTION_MODEL"

	reasonProviderDiscoveryFailed = "provider_discovery_failed"
)

// ModelClient is one discovered, provider-qualified Elelem model.
type ModelClient struct {
	Client *elelem.Client
	Model  elelem.Model
}

// ModelResolver resolves a qualified provider/model reference.
type ModelResolver interface {
	ResolveModel(qualifiedModel string) (ModelClient, error)
}

// DriverFactory builds a driver for one validated provider configuration.
type DriverFactory func(config.Upstream) (elelem.Driver, error)

// Registry contains only models confirmed by their providers' ListModels APIs.
type Registry struct {
	models map[string]ModelClient
}

// RegistryOptions describes one deployment's model discovery.
type RegistryOptions struct {
	Upstreams []config.Upstream

	// DefaultModel and CompactionModel must both resolve, and both must
	// accept MaxContextTokens, before startup may continue. An empty
	// CompactionModel takes DefaultModel.
	DefaultModel    string
	CompactionModel string

	// MaxContextTokens is the deployment's request budget. It supplies the
	// context size for a model whose driver publishes none, and it may not
	// exceed the window of a model that publishes one.
	MaxContextTokens int

	// Factory builds one provider's driver. Nil uses NewDriver.
	Factory DriverFactory
}

// NewRegistry discovers models from every usable provider. Unselected provider
// failures are logged and skipped. The main and compaction references must
// resolve, and must accept the configured budget, before startup may continue.
func NewRegistry(
	ctx context.Context,
	options RegistryOptions,
) (*Registry, error) {
	if options.MaxContextTokens <= 0 {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"registry max context tokens",
		)
	}

	factory := options.Factory
	if factory == nil {
		factory = NewDriver
	}

	registry := &Registry{models: make(map[string]ModelClient)}
	for _, upstream := range options.Upstreams {
		err := registry.discover(
			ctx,
			upstream,
			factory,
			options.MaxContextTokens,
		)
		if err != nil {
			ctxscope.GetLogger(ctx).Warn(
				"provider discovery failed",
				"reason", reasonProviderDiscoveryFailed,
				"provider", upstream.Name,
				"err", err,
			)
		}
	}

	compactionModel := options.CompactionModel
	if compactionModel == "" {
		compactionModel = options.DefaultModel
	}

	selections := []struct {
		setting   string
		reference string
	}{
		{setting: settingDefaultModel, reference: options.DefaultModel},
		{setting: settingCompactionModel, reference: compactionModel},
	}

	for _, selection := range selections {
		err := registry.validateSelection(
			selection.reference,
			options.MaxContextTokens,
		)
		if err != nil {
			return nil, ctxerrors.Wrap(err, selection.setting)
		}
	}

	return registry, nil
}

// validateSelection rejects a budget larger than the model can accept.
//
// A budget above the provider's window is not a bigger context, it is a
// provider error on the first turn that actually fills it, which surfaces as a
// failed turn rather than a failed startup. A model whose driver publishes no
// window is not checked: discovery already gave it this budget as its size.
func (r *Registry) validateSelection(
	qualifiedModel string,
	maxContextTokens int,
) error {
	selected, err := r.ResolveModel(qualifiedModel)
	if err != nil {
		return ctxerrors.Wrap(err, "resolve selected model")
	}

	if selected.Model.ContextSize > 0 &&
		maxContextTokens > selected.Model.ContextSize {
		return ctxerrors.Wrapf(
			ErrContextBudgetTooLarge,
			"%d exceeds the %d token window of %q",
			maxContextTokens,
			selected.Model.ContextSize,
			qualifiedModel,
		)
	}

	return nil
}

// NewStaticRegistry supplies a deterministic model seam for unit and
// integration tests. Every key must be a valid unique qualified reference.
func NewStaticRegistry(models map[string]ModelClient) (*Registry, error) {
	registry := &Registry{models: make(map[string]ModelClient, len(models))}
	for qualifiedModel, model := range models {
		if err := validateModelReference(qualifiedModel); err != nil {
			return nil, ctxerrors.Wrap(err, "validate static model reference")
		}

		if model.Client == nil || model.Model.ID == "" {
			return nil, ctxerrors.Wrap(ErrModelUnavailable, qualifiedModel)
		}

		registry.models[qualifiedModel] = model
	}

	return registry, nil
}

// ResolveModel returns the exact discovered client and raw provider model ID.
func (r *Registry) ResolveModel(qualifiedModel string) (ModelClient, error) {
	if err := validateModelReference(qualifiedModel); err != nil {
		return ModelClient{}, ctxerrors.Wrap(err, "validate model reference")
	}

	model, ok := r.models[qualifiedModel]
	if !ok {
		return ModelClient{}, ctxerrors.Wrapf(
			ErrModelUnavailable,
			"model %q was not discovered",
			qualifiedModel,
		)
	}

	return model, nil
}

// NewDriver builds one environment-isolated Elelem provider driver.
//
//nolint:ireturn // Elelem's driver contract is an interface.
func NewDriver(upstream config.Upstream) (elelem.Driver, error) {
	switch upstream.Provider {
	case config.ProviderTypeOpenAI:
		return newOpenAIDriver(upstream), nil
	case config.ProviderTypeAnthropic:
		return newAnthropicDriver(upstream), nil
	case config.ProviderTypeZAICoding:
		return newZAICodingDriver(upstream), nil
	default:
		return nil, ctxerrors.Wrapf(
			ErrModelUnavailable,
			"unsupported provider %q",
			upstream.Provider,
		)
	}
}

//nolint:ireturn // Elelem's driver contract is an interface.
func newOpenAIDriver(upstream config.Upstream) elelem.Driver {
	options := []openai.DriverOption{
		openai.WithoutEnvironmentDefaults(),
	}
	if upstream.APIKeyEnv != "" {
		options = append(options, openai.WithAPIKey(upstream.APIKey()))
	}

	if upstream.BaseURL != "" {
		options = append(options, openai.WithBaseURL(upstream.BaseURL))
	}

	return withProviderRetry(openai.NewDriver(options...))
}

//nolint:ireturn // Elelem's driver contract is an interface.
func newAnthropicDriver(upstream config.Upstream) elelem.Driver {
	options := []anthropic.DriverOption{
		anthropic.WithoutEnvironmentDefaults(),
	}
	if upstream.APIKeyEnv != "" {
		options = append(options, anthropic.WithAPIKey(upstream.APIKey()))
	}

	if upstream.BaseURL != "" {
		options = append(options, anthropic.WithBaseURL(upstream.BaseURL))
	}

	return withProviderRetry(anthropic.NewDriver(options...))
}

//nolint:ireturn // Elelem's driver contract is an interface.
func newZAICodingDriver(upstream config.Upstream) elelem.Driver {
	options := []zaicoding.DriverOption{}
	if upstream.APIKeyEnv != "" {
		options = append(options, zaicoding.WithAPIKey(upstream.APIKey()))
	}

	if upstream.BaseURL != "" {
		options = append(options, zaicoding.WithBaseURL(upstream.BaseURL))
	}

	return withProviderRetry(zaicoding.NewDriver(options...))
}

//nolint:ireturn // Elelem's retry decorator returns the Driver contract.
func withProviderRetry(driver elelem.Driver) elelem.Driver {
	return withRetryConfig(driver, providerRetryConfig())
}

func providerRetryConfig() elelem.RetryConfig {
	return elelem.RetryConfig{
		MaxAttempts:  providerMaximumAttempts,
		InitialDelay: providerInitialRetryDelay,
		MaxDelay:     providerMaximumRetryDelay,
	}
}

//nolint:ireturn // Elelem's retry decorator returns the Driver contract.
func withRetryConfig(
	driver elelem.Driver,
	config elelem.RetryConfig,
) elelem.Driver {
	return elelem.WithRetry(driver, config)
}

func (r *Registry) discover(
	ctx context.Context,
	upstream config.Upstream,
	factory DriverFactory,
	maxContextTokens int,
) error {
	driver, err := factory(upstream)
	if err != nil {
		return ctxerrors.Wrap(err, "create provider driver")
	}

	models, err := driver.ListModels(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "list provider models")
	}

	for _, modelID := range models {
		if modelID == "" {
			continue
		}

		model := modelMetadata(upstream.Provider, modelID, maxContextTokens)
		qualifiedModel := upstream.Name + "/" + modelID
		r.models[qualifiedModel] = ModelClient{
			Client: elelem.New(driver, elelem.WithDefaultModel(model)),
			Model:  model,
		}
	}

	return nil
}

// modelMetadata returns what the driver publishes about one discovered model.
//
// Building elelem.Model{ID: id} by hand instead discards the context window
// and the reasoning levels, which silently disables every Elelem check that
// depends on them. A model the driver does not know keeps the deployment's own
// budget as its size, which is what makes PEEN_MAX_CONTEXT_TOKENS the explicit
// context size for a gateway or local model no catalog covers.
func modelMetadata(
	provider config.ProviderType,
	modelID string,
	maxContextTokens int,
) elelem.Model {
	var model elelem.Model

	switch provider {
	case config.ProviderTypeOpenAI:
		model = openai.LookupModel(modelID)
	case config.ProviderTypeAnthropic:
		model = anthropic.LookupModel(modelID)
	case config.ProviderTypeZAICoding:
		model = zaicoding.LookupModel(modelID)
	default:
		model = elelem.Model{ID: modelID}
	}

	model.ID = modelID
	if model.ContextSize <= 0 {
		model.ContextSize = maxContextTokens
	}

	return model
}

func validateModelReference(qualifiedModel string) error {
	providerName, modelID, found := strings.Cut(qualifiedModel, "/")
	if !found || providerName == "" || modelID == "" {
		return ctxerrors.Wrapf(
			ErrInvalidModelReference,
			"expected provider/model, got %q",
			qualifiedModel,
		)
	}

	return nil
}

var _ ModelResolver = (*Registry)(nil)
