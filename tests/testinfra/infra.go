// Package testinfra is the extendable integration-test harness for a
// servicepack app. Its baseline builds the servicepack image from the repo
// Dockerfile and runs it, so a test exercises the real, containerized
// application with whatever services it registers, and nothing more.
//
// Extend it with the external dependencies your services need (a database, a
// cache, a broker via testcontainers-go): give Infra a field, start it in
// Setup, wire the app container to it, and tear it down in Teardown. The DIND
// runner already supports this: DEV_RUN_DIND uses the host network, so
// testcontainers' host-published ports are reachable.
package testinfra

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/testcontainers/testcontainers-go"
)

var errNoGoMod = errors.New("go.mod not found above the working directory")

// Infra holds the containers a test package brought up. At the baseline that is
// just the application image; extend it with one field per external dependency
// (for example: Postgres *PostgresResource) as your services grow.
type Infra struct {
	App        testcontainers.Container
	baseURL    string
	image      string
	metricsURL string
	client     *http.Client
	provider   *openAIModelsMock
}

// Setup builds Peen's app image and starts it against a local OpenAI-compatible
// provider mock. API readiness proves production config, migrations, provider
// discovery, and the HTTP service all completed.
func Setup(ctx context.Context) (*Infra, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}

	provider := newOpenAIModelsMock()

	listenAddress, err := reserveLoopbackAddress(ctx)
	if err != nil {
		return setupFailure(provider, err)
	}

	metricsListenAddress, err := reserveLoopbackAddress(ctx)
	if err != nil {
		return setupFailure(provider, err)
	}

	return startApp(
		ctx,
		root,
		provider,
		listenAddress,
		metricsListenAddress,
	)
}

func setupFailure(provider *openAIModelsMock, err error) (*Infra, error) {
	provider.server.Close()

	return nil, err
}

func failedContainerSetup(
	ctx context.Context,
	container testcontainers.Container,
	image string,
	provider *openAIModelsMock,
	err error,
) (*Infra, error) {
	var cleanupErr error

	if container != nil || image != "" {
		cleanupCtx, cancelCleanup := context.WithTimeout(
			context.WithoutCancel(ctx),
			appCleanupTimeout,
		)
		defer cancelCleanup()

		if container != nil {
			terminateErr := container.Terminate(cleanupCtx)
			if terminateErr != nil {
				cleanupErr = ctxerrors.Wrap(
					terminateErr,
					"terminate failed app container",
				)
			}
		}

		imageRemoveErr := removeAppImage(cleanupCtx, image)
		if imageRemoveErr != nil {
			cleanupErr = errors.Join(
				cleanupErr,
				ctxerrors.Wrap(
					imageRemoveErr,
					"remove app image after setup failure",
				),
			)
		}
	}

	provider.server.Close()

	return nil, errors.Join(
		ctxerrors.Wrap(err, "build and start servicepack image"),
		cleanupErr,
	)
}

// Teardown terminates every started container. It is safe to call after a
// partially completed setup and returns any application cleanup failure.
func (i *Infra) Teardown(ctx context.Context) error {
	var teardownErr error

	if i.App != nil {
		if err := i.App.Terminate(ctx); err != nil {
			teardownErr = ctxerrors.Wrap(err, "terminate app container")
		}
	}

	if err := removeAppImage(ctx, i.image); err != nil {
		teardownErr = errors.Join(
			teardownErr,
			ctxerrors.Wrap(err, "remove app image"),
		)
	}

	if i.provider != nil {
		i.provider.server.Close()
	}

	return teardownErr
}

func wrapOptionalError(err error, message string) error {
	if err == nil {
		return nil
	}

	return ctxerrors.Wrap(err, message)
}

// Restart stops and starts the production application container, then waits
// for its already-configured HTTP listener to become ready again.
func (i *Infra) Restart(ctx context.Context) error {
	if i == nil || i.App == nil {
		return ctxerrors.New("application container is unavailable")
	}

	timeout := appRestartTimeout
	if err := i.App.Stop(ctx, &timeout); err != nil {
		return ctxerrors.Wrap(err, "stop application container")
	}

	if err := i.App.Start(ctx); err != nil {
		return ctxerrors.Wrap(err, "start application container")
	}

	return i.waitForReady(ctx)
}

// CrashAndRestart kills this fixture's container without graceful cleanup,
// then starts it with the same files and waits for HTTP readiness.
func (i *Infra) CrashAndRestart(ctx context.Context) (resultErr error) {
	if i == nil || i.App == nil {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"application container is unavailable")
	}

	ctx, cancel := context.WithTimeout(ctx, appRestartTimeout)
	defer cancel()

	dockerClient, err := newDockerClient()
	if err != nil {
		return ctxerrors.Wrap(err, "connect to fixture container daemon")
	}
	defer func() {
		resultErr = errors.Join(resultErr, closeDockerClient(dockerClient))
	}()

	// App is the exact container Setup created for this test suite.
	id := i.App.GetContainerID()
	if _, err := dockerClient.ContainerKill(ctx, id,
		client.ContainerKillOptions{Signal: appCrashSignal}); err != nil {
		return ctxerrors.Wrap(err, "kill fixture application container")
	}

	wait := dockerClient.ContainerWait(ctx, id, client.ContainerWaitOptions{
		Condition: container.WaitConditionNotRunning,
	})
	select {
	case stopped := <-wait.Result:
		if stopped.Error != nil {
			return ctxerrors.Wrap(commerr.ErrInvalidState,
				"fixture container exit reported an error")
		}
	case err := <-wait.Error:
		return ctxerrors.Wrap(err, "wait for killed fixture container")
	case <-ctx.Done():
		return ctxerrors.Wrap(ctx.Err(), "wait for fixture container exit")
	}

	if err := i.App.Start(ctx); err != nil {
		return ctxerrors.Wrap(err, "restart killed fixture container")
	}

	return i.waitForReady(ctx)
}

// WriteWorkspaceFile adds or replaces one relative fixture file below the
// production container's configured default workspace.
func (i *Infra) WriteWorkspaceFile(
	ctx context.Context,
	name string,
	content []byte,
) error {
	if i == nil || i.App == nil {
		return ctxerrors.New("application container is unavailable")
	}

	target, err := workspaceFilePath(name)
	if err != nil {
		return err
	}

	if err := i.App.CopyToContainer(
		ctx,
		content,
		target,
		appFixtureFileMode,
	); err != nil {
		return ctxerrors.Wrap(err, "write application workspace fixture")
	}

	return nil
}

// CopyStateFiles copies named files from the container's state directory into
// directory. A named file that does not exist is skipped, because SQLite's
// write-ahead files come and go.
func (i *Infra) CopyStateFiles(
	ctx context.Context,
	directory string,
	names ...string,
) error {
	if i == nil || i.App == nil {
		return ctxerrors.New("application container is unavailable")
	}

	for _, name := range names {
		source, err := i.App.CopyFileFromContainer(
			ctx,
			path.Join(appStateDirectory, name),
		)
		if err != nil {
			continue
		}

		content, err := io.ReadAll(source)
		if closeErr := source.Close(); closeErr != nil {
			return ctxerrors.Wrapf(closeErr, "close state file %q", name)
		}

		if err != nil {
			return ctxerrors.Wrapf(err, "read state file %q", name)
		}

		if err := os.WriteFile(
			filepath.Join(directory, name),
			content,
			stateFileCopyMode,
		); err != nil {
			return ctxerrors.Wrapf(err, "write state file %q", name)
		}
	}

	return nil
}

// LastSystemPrompt returns the most recent provider request's system prompt.
func (i *Infra) LastSystemPrompt() string {
	if i == nil || i.provider == nil {
		return ""
	}

	return i.provider.systemPrompt()
}

// LastReasoningEffort returns the reasoning_effort of the most recent provider
// request, or an empty string when the request carried none.
func (i *Infra) LastReasoningEffort() string {
	if i == nil || i.provider == nil {
		return ""
	}

	return i.provider.reasoningEffort()
}

// DefaultModel returns the qualified model reference the production container
// runs a turn on when a message names none.
func (i *Infra) DefaultModel() string {
	return providerModelReference
}

// ModelDiscoveryObserved reports whether the app called the configured models
// endpoint during startup.
func (i *Infra) ModelDiscoveryObserved() bool {
	return i.provider != nil && i.provider.modelsListed.Load()
}

// EnableScriptedToolTurn makes every subsequent provider completion follow
// script's fixed tool conversation instead
// of the default single fixed text response. Off by default, so a test that
// never calls this sees byte-identical behavior to before this method
// existed. Call DisableScriptedToolTurn (for example via t.Cleanup) once the
// scripted conversation is over, so later tests see the default behavior
// again.
func (i *Infra) EnableScriptedToolTurn(script ScriptedToolTurn) {
	i.provider.setScript(&script)
}

// DisableScriptedToolTurn restores the default fixed-text completion.
func (i *Infra) DisableScriptedToolTurn() {
	i.provider.setScript(nil)
}

// APIURL returns the production API endpoint reachable from the Go test
// process.
func (i *Infra) APIURL(path string) string {
	return i.baseURL + path
}

func (i *Infra) waitForReady(ctx context.Context) error {
	for {
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			i.APIURL(appReadyPath),
			nil,
		)
		if err != nil {
			return ctxerrors.Wrap(err, "create readiness request")
		}

		response, requestErr := i.client.Do(request)
		if requestErr == nil {
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && closeErr == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctxerrors.Wrap(ctx.Err(), "wait for application readiness")
		case <-time.After(appRestartPollInterval):
		}
	}
}

func workspaceFilePath(name string) (string, error) {
	cleaned := filepath.Clean(name)
	if cleaned == "." || cleaned == ".." || filepath.IsAbs(cleaned) ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", ctxerrors.New("workspace fixture path must be relative")
	}

	return filepath.Join(appWorkingDirectory, cleaned), nil
}

// MetricsURL returns the private metrics endpoint reachable from the Go test
// process over the application container's loopback listener.
func (i *Infra) MetricsURL(path string) string {
	return i.metricsURL + path
}

// HTTPClient returns the client used to drive the production API container.
func (i *Infra) HTTPClient() *http.Client {
	return i.client
}

// AppLogs returns the current production container logs for failed API-test
// diagnostics. Test code must use them only to explain a failed contract.
func (i *Infra) AppLogs(ctx context.Context) (string, error) {
	if i.App == nil {
		return "", ctxerrors.New("app container is unavailable")
	}

	reader, err := i.App.Logs(ctx)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read app container logs")
	}

	data, readErr := io.ReadAll(reader)

	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return "", errors.Join(
			wrapOptionalError(readErr, "read app container logs"),
			wrapOptionalError(closeErr, "close app container logs"),
		)
	}

	return string(data), nil
}

// HoldNextCompletion blocks one provider completion until Release is called or
// the application cancels the upstream request.
func (i *Infra) HoldNextCompletion() (*CompletionHold, error) {
	if i.provider == nil {
		return nil, ctxerrors.New("provider mock is unavailable")
	}

	return i.provider.HoldNextCompletion()
}
