package testinfra

import (
	"context"
	"maps"
	"net/http"

	"github.com/psyb0t/ctxerrors"
	"github.com/testcontainers/testcontainers-go"
)

// VariantOptions changes the environment of a second application container
// started from the image the base Infra built.
type VariantOptions struct {
	// Environment adds or replaces application environment variables.
	Environment map[string]string
	// UnsetEnvironment removes variables the base environment sets.
	UnsetEnvironment []string
}

// StartVariant starts another container from the base image with its own
// provider mock, listeners, and state, so a test can exercise configuration
// the base container does not carry. The variant shares the image, so its
// Teardown leaves the image to the base Infra's Teardown.
func (i *Infra) StartVariant(
	ctx context.Context,
	options VariantOptions,
) (*Infra, error) {
	if i == nil || i.image == "" {
		return nil, ctxerrors.New("application image is unavailable")
	}

	root, err := repoRoot()
	if err != nil {
		return nil, err
	}

	coverage, err := appCoverageFor(root)
	if err != nil {
		return nil, err
	}

	provider := newOpenAIModelsMock()

	environment, listenAddress, metricsAddress, err := variantEnvironment(
		ctx,
		provider,
		options,
	)
	if err != nil {
		return setupFailure(provider, err)
	}

	container, err := testcontainers.GenericContainer(ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: appContainerRequest(
				i.image,
				environment,
				coverage,
			),
			Started: false,
		},
	)
	if err != nil {
		return failedContainerSetup(ctx, container, "", provider, err)
	}

	if err := container.Start(ctx); err != nil {
		return failedContainerSetup(ctx, container, "", provider, err)
	}

	variant := &Infra{
		App:        container,
		baseURL:    appURLPrefix + listenAddress,
		metricsURL: appURLPrefix + metricsAddress,
		client:     &http.Client{Timeout: appRequestTimeout},
		provider:   provider,
	}
	if err := variant.waitForReady(ctx); err != nil {
		return failedContainerSetup(ctx, container, "", provider, err)
	}

	return variant, nil
}

// variantEnvironment returns the base application environment with the
// options applied, and the two listener addresses it reserved.
func variantEnvironment(
	ctx context.Context,
	provider *openAIModelsMock,
	options VariantOptions,
) (map[string]string, string, string, error) {
	listenAddress, err := reserveLoopbackAddress(ctx)
	if err != nil {
		return nil, "", "", err
	}

	metricsAddress, err := reserveLoopbackAddress(ctx)
	if err != nil {
		return nil, "", "", err
	}

	environment, err := appEnvironment(
		provider.server.URL,
		listenAddress,
		metricsAddress,
	)
	if err != nil {
		return nil, "", "", err
	}

	for _, name := range options.UnsetEnvironment {
		delete(environment, name)
	}

	maps.Copy(environment, options.Environment)

	return environment, listenAddress, metricsAddress, nil
}
