package testinfra

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/jsonmessage"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/testcontainers/testcontainers-go"
)

type appCoverage struct {
	hostDirectory string
}

func startApp(
	ctx context.Context,
	root string,
	provider *openAIModelsMock,
	listenAddress string,
	metricsListenAddress string,
) (*Infra, error) {
	startupCtx, cancelStartup := context.WithTimeout(ctx, appBootTimeout)
	defer cancelStartup()

	environment, err := appEnvironment(
		provider.server.URL,
		listenAddress,
		metricsListenAddress,
	)
	if err != nil {
		return setupFailure(provider, err)
	}

	coverage, err := appCoverageFor(root)
	if err != nil {
		return setupFailure(provider, err)
	}

	request := appImageBuildRequest(root, coverage)

	image, err := retryAppImageBuild(startupCtx, appImageBuildAttempts,
		appImageBuildBackoff, func(ctx context.Context) (string, error) {
			return buildAppImage(ctx, request)
		})
	if err != nil {
		return failedContainerSetup(startupCtx, nil, image, provider, err)
	}

	container, err := testcontainers.GenericContainer(startupCtx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: appContainerRequest(image, environment, coverage),
			Started:          false,
		},
	)
	if err != nil {
		return failedContainerSetup(startupCtx, container, image, provider, err)
	}

	if err := container.Start(startupCtx); err != nil {
		return failedContainerSetup(startupCtx, container, image, provider, err)
	}

	infra := &Infra{
		App:        container,
		baseURL:    appURLPrefix + listenAddress,
		image:      image,
		metricsURL: appURLPrefix + metricsListenAddress,
		client:     &http.Client{Timeout: appRequestTimeout},
		provider:   provider,
	}
	if err := infra.waitForReady(startupCtx); err != nil {
		return failedContainerSetup(startupCtx, container, image, provider, err)
	}

	return infra, nil
}

func appContainerRequest(
	image string,
	environment map[string]string,
	coverage appCoverage,
) testcontainers.ContainerRequest {
	request := testcontainers.ContainerRequest{
		Image:      image,
		Entrypoint: []string{"/bin/sh", "-ceu", appBootstrapCommand},
		Env:        environment,
		WorkingDir: appWorkingDirectory,
		Files: []testcontainers.ContainerFile{
			{
				Reader:            strings.NewReader(ConfigInstructions),
				ContainerFilePath: appFixtureInstructionsPath,
				FileMode:          appFixtureFileMode,
			},
			{
				Reader:            strings.NewReader(appAgentDocument),
				ContainerFilePath: appFixtureAgentPath,
				FileMode:          appFixtureFileMode,
			},
		},
		NetworkMode: container.NetworkMode(appHostNetwork),
	}
	if coverage.hostDirectory == "" {
		return request
	}

	request.Env[appCoverageEnvironment] = appCoverageDirectory
	request.HostConfigModifier = func(hostConfig *container.HostConfig) {
		hostConfig.NetworkMode = container.NetworkMode(appHostNetwork)
		hostConfig.Binds = append(
			hostConfig.Binds,
			coverage.hostDirectory+":"+appCoverageDirectory,
		)
	}
	request.User = strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())

	return request
}

func appImageBuildRequest(
	root string,
	coverage appCoverage,
) testcontainers.ContainerRequest {
	request := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    root,
			Dockerfile: appDockerfile,
		},
	}
	if coverage.hostDirectory == "" {
		return request
	}

	coverageBuildValue := appCoverageEnabled
	request.BuildArgs = map[string]*string{
		appCoverageBuildArgument: &coverageBuildValue,
	}

	return request
}

func appImageBuildOptions(
	request *testcontainers.ContainerRequest,
	image string,
) client.ImageBuildOptions {
	return client.ImageBuildOptions{
		BuildArgs:   request.GetBuildArgs(),
		Dockerfile:  request.GetDockerfile(),
		ForceRemove: true,
		Remove:      true,
		Tags:        []string{image},
		Version:     build.BuilderV1,
	}
}

// retryAppImageBuild runs build up to attempts times, waiting backoff before
// the second attempt and doubling it after each failure. The build reaches
// the network for base images and packages, and one blip on a CI runner used
// to fail the whole suite. It stops early when ctx ends.
func retryAppImageBuild(
	ctx context.Context,
	attempts int,
	backoff time.Duration,
	build func(context.Context) (string, error),
) (string, error) {
	logger := ctxscope.GetLogger(ctx)
	delay := backoff

	for attempt := 1; ; attempt++ {
		image, err := build(ctx)
		if err == nil {
			return image, nil
		}

		if attempt >= attempts {
			return "", ctxerrors.Wrapf(err,
				"build app image after %d attempts", attempt)
		}

		logger.Warn("integration image build failed, retrying",
			"err", err, "attempt", attempt, "delay", delay,
			"reason", "image_build_retry")

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()

			return "", ctxerrors.Wrap(errors.Join(err, ctx.Err()),
				"wait to retry app image build")
		case <-timer.C:
		}

		delay *= 2
	}
}

// buildAppImage keeps the Docker client alive until Docker consumes the full
// build context. Testcontainers closes its client before the Docker 29 daemon
// has finished receiving a BuildKit context, which aborts source-built tests.
func buildAppImage(
	ctx context.Context,
	request testcontainers.ContainerRequest,
) (string, error) {
	logger := ctxscope.GetLogger(ctx)
	logger.Info("integration image build context started")

	buildContext, err := request.GetContext()
	if err != nil {
		return "", ctxerrors.Wrap(err, "create app image build context")
	}

	image := request.GetRepo() + ":" + request.GetTag()
	logger.Info("integration image build started", "image", image)

	dockerClient, err := newDockerClient()
	if err != nil {
		return "", errors.Join(err, closeAppImageBuildContext(buildContext))
	}

	response, err := dockerClient.ImageBuild(
		ctx,
		buildContext,
		appImageBuildOptions(&request, image),
	)
	if err != nil {
		return "", errors.Join(
			ctxerrors.Wrap(err, "build app image"),
			closeAppImageBuildContext(buildContext),
			closeDockerClient(dockerClient),
		)
	}

	buildErr := jsonmessage.DisplayStream(response.Body, os.Stderr)
	responseCloseErr := response.Body.Close()
	buildContextCloseErr := closeAppImageBuildContext(buildContext)

	dockerClientCloseErr := closeDockerClient(dockerClient)
	if buildErr != nil ||
		responseCloseErr != nil ||
		buildContextCloseErr != nil ||
		dockerClientCloseErr != nil {
		return "", errors.Join(
			wrapOptionalError(buildErr, "read app image build result"),
			wrapOptionalError(responseCloseErr, "close app image build result"),
			buildContextCloseErr,
			dockerClientCloseErr,
		)
	}

	return image, nil
}

func removeAppImage(ctx context.Context, image string) error {
	if image == "" {
		return nil
	}

	dockerClient, err := newDockerClient()
	if err != nil {
		return err
	}

	_, removeErr := dockerClient.ImageRemove(
		ctx,
		image,
		client.ImageRemoveOptions{},
	)

	return errors.Join(
		wrapOptionalError(removeErr, "remove app image"),
		closeDockerClient(dockerClient),
	)
}

func newDockerClient() (*client.Client, error) {
	dockerClient, err := client.New(client.FromEnv)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create Docker client")
	}

	return dockerClient, nil
}

func closeAppImageBuildContext(buildContext io.Reader) error {
	closer, ok := buildContext.(io.Closer)
	if !ok {
		return nil
	}

	if err := closer.Close(); err != nil {
		return ctxerrors.Wrap(err, "close app image build context")
	}

	return nil
}

func closeDockerClient(dockerClient *client.Client) error {
	if err := dockerClient.Close(); err != nil {
		return ctxerrors.Wrap(err, "close Docker client")
	}

	return nil
}

func appCoverageFor(root string) (appCoverage, error) {
	configuredDirectory := os.Getenv(coverageDirectoryEnv)
	if configuredDirectory == "" {
		return appCoverage{}, nil
	}

	rootDirectory, err := filepath.Abs(root)
	if err != nil {
		return appCoverage{}, ctxerrors.Wrap(err, "resolve application root")
	}

	coverageDirectory, err := filepath.Abs(configuredDirectory)
	if err != nil {
		return appCoverage{}, ctxerrors.Wrap(err, "resolve coverage directory")
	}

	relativeDirectory, err := filepath.Rel(rootDirectory, coverageDirectory)
	if err != nil {
		return appCoverage{}, ctxerrors.Wrap(
			err,
			"check coverage directory scope",
		)
	}

	if relativeDirectory == ".." || strings.HasPrefix(
		relativeDirectory,
		".."+string(filepath.Separator),
	) {
		return appCoverage{}, ctxerrors.New(
			"coverage directory must stay within the application root",
		)
	}

	info, err := os.Stat(coverageDirectory)
	if err != nil {
		return appCoverage{}, ctxerrors.Wrap(err, "inspect coverage directory")
	}

	if !info.IsDir() {
		return appCoverage{}, ctxerrors.New("coverage path must be a directory")
	}

	return appCoverage{hostDirectory: coverageDirectory}, nil
}

func appEnvironment(
	providerBaseURL string,
	listenAddress string,
	metricsListenAddress string,
) (map[string]string, error) {
	upstreams, err := json.Marshal([]map[string]string{{
		"name":      providerName,
		"type":      providerType,
		"baseUrl":   providerBaseURL,
		"apiKeyEnv": ProviderKeyEnvName,
	}})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "marshal test provider configuration")
	}

	return map[string]string{
		"PEEN_MAX_CONCURRENT_AGENT_RUNS": "7",
		"PEEN_CONFIG_DIR":                appConfigDirectory,
		"PEEN_STATE_DIR":                 appStateDirectory,
		"PEEN_AGENT":                     appAgentName,
		"PEEN_CRASH_RECOVERY":            appCrashRecovery,
		"PEEN_HTTP_LISTEN_ADDRESS":       listenAddress,
		"PEEN_METRICS_LISTEN_ADDRESS":    metricsListenAddress,
		"PEEN_UPSTREAMS":                 string(upstreams),
		"PEEN_DEFAULT_MODEL":             providerModelReference,
		"PEEN_API_TOKEN":                 TestAPIToken,
		"PEEN_SYSTEM_AGENT":              "true",
		// A test workspace configures its own MCP servers, so a server one
		// test starts never reaches the tool list of another.
		"PEEN_WORKSPACE_MCP": "true",
		ProviderKeyEnvName:   TestProviderKey,
	}, nil
}

func reserveLoopbackAddress(ctx context.Context) (string, error) {
	listenerConfig := net.ListenConfig{}

	listener, err := listenerConfig.Listen(
		ctx,
		appNetwork,
		appLoopbackAddress,
	)
	if err != nil {
		return "", ctxerrors.Wrap(err, "reserve loopback API address")
	}

	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", ctxerrors.Wrap(err, "release loopback API address")
	}

	return address, nil
}

// repoRoot walks up from the test's working directory to the module root (the
// directory holding go.mod), which is where the Dockerfile and its build
// context live.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", ctxerrors.Wrap(err, "get working directory")
	}

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errNoGoMod
		}

		dir = parent
	}
}
