//go:build browser

package browser_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

//nolint:gochecknoglobals // TestMain owns the shared infra and browser, and every test reads them.
var (
	integrationInfra *testinfra.Infra
	stealthyBrowser  *browserClient
	browserContainer testcontainers.Container
)

func TestMain(m *testing.M) {
	setupContext, cancelSetup := context.WithTimeout(
		context.Background(),
		browserTestTimeout,
	)

	infra, err := testinfra.Setup(setupContext)
	if err != nil {
		cancelSetup()
		writeTestMainError("set up Peen browser integration test", err)
		os.Exit(1)
	}
	integrationInfra = infra

	browser, client, err := startBrowser(setupContext)
	cancelSetup()
	if err != nil {
		teardownContext, cancelTeardown := context.WithTimeout(
			context.Background(),
			browserCleanupTimeout,
		)
		if teardownErr := integrationInfra.Teardown(teardownContext); teardownErr != nil {
			writeTestMainError("tear down Peen browser integration test", teardownErr)
		}
		cancelTeardown()
		writeTestMainError("set up Stealthy browser integration test", err)
		os.Exit(1)
	}
	browserContainer = browser
	stealthyBrowser = client

	exitCode := m.Run()
	teardownContext, cancelTeardown := context.WithTimeout(
		context.Background(),
		browserCleanupTimeout,
	)
	if err := browserContainer.Terminate(teardownContext); err != nil {
		writeTestMainError("tear down Stealthy browser integration test", err)
		if exitCode == 0 {
			exitCode = 1
		}
	}
	if err := integrationInfra.Teardown(teardownContext); err != nil {
		writeTestMainError("tear down Peen browser integration test", err)
		if exitCode == 0 {
			exitCode = 1
		}
	}
	cancelTeardown()

	os.Exit(exitCode)
}

//nolint:ireturn // testcontainers hands the started container back as an interface.
func startBrowser(ctx context.Context) (testcontainers.Container, *browserClient, error) {
	address, err := reserveLoopbackAddress(ctx)
	if err != nil {
		return nil, nil, err
	}

	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "split Stealthy browser listener address")
	}

	startupContext, cancelStartup := context.WithTimeout(ctx, browserStartupTimeout)
	defer cancelStartup()

	browser, err := testcontainers.GenericContainer(startupContext,
		testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image: browserImage,
				Env: map[string]string{
					browserHTTPListenHostEnv: browserListenHost,
					browserHTTPListenPortEnv: port,
				},
				NetworkMode: container.NetworkMode(browserHostNetwork),
			},
			Started: true,
		},
	)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "start Stealthy browser container")
	}

	client := &browserClient{
		baseURL: browserURLPrefix + address,
		client:  &http.Client{Timeout: browserRequestTimeout},
	}
	if err := client.waitForReady(startupContext); err != nil {
		cleanupContext, cancelCleanup := context.WithTimeout(
			context.WithoutCancel(ctx),
			browserCleanupTimeout,
		)
		defer cancelCleanup()

		return nil, nil, errors.Join(
			ctxerrors.Wrap(err, "wait for Stealthy browser readiness"),
			wrapOptionalError(
				browser.Terminate(cleanupContext),
				"terminate failed Stealthy browser container",
			),
		)
	}

	return browser, client, nil
}

func reserveLoopbackAddress(ctx context.Context) (string, error) {
	listenerConfig := net.ListenConfig{}
	listener, err := listenerConfig.Listen(ctx, "tcp", browserLoopbackAddress)
	if err != nil {
		return "", ctxerrors.Wrap(err, "reserve Stealthy browser listener address")
	}

	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", ctxerrors.Wrap(err, "release Stealthy browser listener address")
	}

	return address, nil
}

func assertNoDockerSocketMount(t *testing.T, target testcontainers.Container) {
	t.Helper()

	inspection, err := target.Inspect(t.Context())
	require.NoError(t, err)
	for _, mount := range inspection.Mounts {
		require.NotEqual(t, dockerSocketDestination, mount.Destination)
	}
}

func wrapOptionalError(err error, message string) error {
	if err == nil {
		return nil
	}

	return ctxerrors.Wrap(err, message)
}

func writeTestMainError(stage string, err error) {
	// TestMain cannot return an error, and a write failure cannot be recovered.
	_, _ = fmt.Fprintln(os.Stderr, stage+":", err)
}
