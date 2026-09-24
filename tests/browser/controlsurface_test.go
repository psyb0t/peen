//go:build browser

package browser_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

const (
	browserTestTimeout                   = 25 * time.Minute
	browserStartupTimeout                = 2 * time.Minute
	browserCleanupTimeout                = 30 * time.Second
	browserRequestTimeout                = 30 * time.Second
	browserPollInterval                  = 100 * time.Millisecond
	browserLoopbackAddress               = "127.0.0.1:0"
	browserListenHost                    = "127.0.0.1"
	browserURLPrefix                     = "http://"
	browserHealthPath                    = "/health"
	browserCommandPath                   = "/"
	browserScreenshotPath                = "/screenshot/browser?whLargest=512"
	browserHostNetwork                   = "host"
	browserHTTPListenHostEnv             = "HTTP_LISTEN_HOST"
	browserHTTPListenPortEnv             = "HTTP_LISTEN_PORT"
	browserArtifactRootEnv               = "PEEN_BROWSER_TEST_ARTIFACT_ROOT"
	browserLogPrefix                     = "peen.browser "
	browserActionKey                     = "action"
	browserActionConsoleLog              = "enable_console_log"
	browserActionNetworkLog              = "enable_network_log"
	browserActionGoto                    = "goto"
	browserActionWaitForElement          = "wait_for_element"
	browserActionWaitForText             = "wait_for_text"
	browserActionFill                    = "fill"
	browserActionClick                   = "click"
	browserActionGetText                 = "get_text"
	browserActionGetElement              = "get_element"
	browserActionNetworkRead             = "get_network_log"
	browserActionConsoleRead             = "get_console_log"
	browserWaitUntilKey                  = "wait_until"
	browserWaitUntilDOM                  = "domcontentloaded"
	browserTimeoutKey                    = "timeout"
	browserTextKey                       = "text"
	browserSelectorKey                   = "selector"
	browserValueKey                      = "value"
	browserSelectorToken                 = "form.connection input[type=password]"
	browserSelectorConnect               = "form.connection button[type=submit]"
	browserSelectorOpen                  = "form.workspace-form button[type=submit]:not(:disabled)"
	browserSelectorWorkspace             = "form.workspace-form input[list=workspace-roots]"
	browserSelectorMessage               = "form.composer textarea"
	browserSelectorSend                  = "form.composer button[type=submit]"
	browserSelectorDetails               = "aside.inspector"
	browserSelectorSocketOpen            = "p.connection-state.connected"
	browserEventKey                      = "event"
	browserNetworkRequest                = "request"
	browserNetworkResponse               = "response"
	browserConsoleError                  = "error"
	browserWebSocketPath                 = "/v1/ws"
	browserModelsPath                    = "/v1/models"
	browserWorkspaceRootsPath            = "/v1/workspace-roots"
	browserSessionsOpenPath              = "/v1/sessions/open"
	browserMessagesPath                  = "/v1/messages"
	browserTestWorkspace                 = "/tmp"
	browserChildWorkspaceName            = "browser-child-workspace"
	browserChildWorkspace                = browserTestWorkspace + "/" + browserChildWorkspaceName
	browserBrokenSkillFixture            = browserChildWorkspaceName + "/.agents/skills/broken-skill/ignored.txt"
	browserBrokenSkillContent            = "this is not a skill document\n"
	browserHarnessWarningLabel           = "Workspace configuration warning"
	browserBrokenSkillWarningSource      = "skill at /tmp/browser-child-workspace/.agents/skills/broken-skill:"
	browserBrokenSkillWarningReason      = "has no SKILL.md"
	browserThinkingMessage               = "browser control surface thinking fixture"
	browserThinkingCompletion            = "integration completion 1"
	browserTestMessage                   = "browser control surface fixture message"
	browserCompletionText                = "integration completion"
	browserFixtureName                   = "browser-control-surface.md"
	browserFixturePath                   = testinfra.ContainerWorkingDirectory + "/" + browserFixtureName
	browserFixtureContent                = "before\n"
	browserFixturePatch                  = "*** Begin Patch\n*** Update File: /tmp/browser-control-surface.md\n@@\n-before\n+after\n*** End Patch"
	browserFixtureCommand                = "test -f /tmp/browser-control-surface.md"
	browserFixtureCommandPurpose         = "confirm the browser fixture remains available"
	browserMainHeading                   = "Code with a durable agent."
	browserThinkingLabel                 = "Thinking"
	browserToolReadFile                  = "read_file"
	browserToolApplyPatch                = "apply_patch"
	browserToolRunCommand                = "run_command"
	browserToolOutputLabel               = "Tool output"
	browserLiveEventsLabel               = "Live events ("
	browserModelReference                = "integration/test-model"
	browserMaxScreenshotEdge             = 512
	browserImage                         = "psyb0t/stealthy-auto-browse@sha256:d481011eff9432a3afe7b03d06634eca1a1061f3ea1e17d99d0e18f7fcac425f"
	dockerSocketDestination              = "/var/run/docker.sock"
	browserDiagnosticSocketConnect       = "socket.connect.start"
	browserDiagnosticSocketOpen          = "socket.open"
	browserDiagnosticControllerStart     = "controller.refresh.start"
	browserDiagnosticControllerComplete  = "controller.refresh.complete"
	browserDiagnosticWorkspaceOpenStart  = "workspace.open.start"
	browserDiagnosticWorkspaceOpenDone   = "workspace.open.complete"
	browserDiagnosticSessionLoadStart    = "session.load.start"
	browserDiagnosticSessionLoadDone     = "session.load.complete"
	browserDiagnosticMessageSendStart    = "message.send.start"
	browserDiagnosticSocketMessageSent   = "socket.message.sent"
	browserDiagnosticSocketEventReceived = "socket.event.received"
)

var expectedBrowserDiagnostics = []string{
	browserDiagnosticSocketConnect,
	browserDiagnosticSocketOpen,
	browserDiagnosticControllerStart,
	browserDiagnosticControllerComplete,
	browserDiagnosticWorkspaceOpenStart,
	browserDiagnosticWorkspaceOpenDone,
	browserDiagnosticSessionLoadStart,
	browserDiagnosticSessionLoadDone,
	browserDiagnosticMessageSendStart,
	browserDiagnosticSocketMessageSent,
	browserDiagnosticSocketEventReceived,
}

var (
	integrationInfra *testinfra.Infra
	stealthyBrowser  *browserClient
	browserContainer testcontainers.Container
)

type browserClient struct {
	baseURL string
	client  *http.Client
}

type browserResponse struct {
	Data    json.RawMessage `json:"data"`
	Success bool            `json:"success"`
}

type browserLogResponse struct {
	Count int          `json:"count"`
	Log   []browserLog `json:"log"`
}

type browserLog struct {
	Method string `json:"method"`
	Status int    `json:"status"`
	Text   string `json:"text"`
	Type   string `json:"type"`
	URL    string `json:"url"`
}

type browserTextResponse struct {
	Text string `json:"text"`
}

type browserElementResponse struct {
	Text string `json:"text"`
}

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

func TestControlSurfaceCompletesATurnInARealBrowser(t *testing.T) {
	assertNoDockerSocketMount(t, browserContainer)

	ctx := t.Context()
	requireWorkspaceRootAvailable(t, ctx)
	browserAction(t, ctx, browserActionConsoleLog, nil)
	browserAction(t, ctx, browserActionNetworkLog, nil)
	t.Cleanup(func() {
		reportBrowserFailureState(t)
	})
	browserAction(t, ctx, browserActionGoto, map[string]any{
		"url":               integrationInfra.APIURL("/"),
		browserWaitUntilKey: browserWaitUntilDOM,
	})
	browserAction(t, ctx, browserActionWaitForText, map[string]any{
		browserTextKey:    browserMainHeading,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})

	browserAction(t, ctx, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorToken,
		browserValueKey:    testinfra.TestAPIToken,
	})
	browserAction(t, ctx, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorConnect,
	})
	browserAction(t, ctx, browserActionWaitForElement, map[string]any{
		browserSelectorKey: browserSelectorOpen,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})
	browserAction(t, ctx, browserActionWaitForElement, map[string]any{
		browserSelectorKey: browserSelectorSocketOpen,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		ctx,
		browserBrokenSkillFixture,
		[]byte(browserBrokenSkillContent),
	))
	browserAction(t, ctx, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorWorkspace,
		browserValueKey:    browserChildWorkspace,
	})
	browserAction(t, ctx, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorOpen,
	})
	browserAction(t, ctx, browserActionWaitForText, map[string]any{
		browserTextKey:    browserModelReference,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})

	browserAction(t, ctx, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorMessage,
		browserValueKey:    browserThinkingMessage,
	})
	browserAction(t, ctx, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorSend,
	})
	browserAction(t, ctx, browserActionWaitForText, map[string]any{
		browserTextKey:    browserThinkingCompletion,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})
	browserAction(t, ctx, browserActionWaitForText, map[string]any{
		browserTextKey:    browserHarnessWarningLabel,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})
	browserAction(t, ctx, browserActionWaitForText, map[string]any{
		browserTextKey:    browserBrokenSkillWarningSource,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})

	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		ctx,
		browserFixtureName,
		[]byte(browserFixtureContent),
	))
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: browserTestMessage,
		ReadFileArguments: map[string]any{
			"path": browserFixturePath,
		},
		ApplyPatchArguments: map[string]any{
			"patch": browserFixturePatch,
		},
		RunCommandArguments: map[string]any{
			"command": browserFixtureCommand,
			"purpose": browserFixtureCommandPurpose,
		},
		FinalAnswer: browserCompletionText,
	})
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)

	browserAction(t, ctx, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorMessage,
		browserValueKey:    browserTestMessage,
	})
	browserAction(t, ctx, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorSend,
	})
	browserAction(t, ctx, browserActionWaitForText, map[string]any{
		browserTextKey:    browserCompletionText,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})
	browserAction(t, ctx, browserActionWaitForText, map[string]any{
		browserTextKey:    browserToolOutputLabel,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})

	page := browserText(t, ctx)
	require.Contains(t, page, browserThinkingMessage)
	require.Contains(t, page, browserThinkingCompletion)
	require.Contains(t, page, browserTestMessage)
	require.Contains(t, page, browserCompletionText)
	require.Contains(t, page, browserModelReference)
	require.Contains(t, page, browserThinkingLabel)
	require.Contains(t, page, browserHarnessWarningLabel)
	require.Contains(t, page, browserBrokenSkillWarningSource)
	require.Contains(t, page, browserBrokenSkillWarningReason)
	require.Contains(t, page, browserToolReadFile)
	require.Contains(t, page, browserToolApplyPatch)
	require.Contains(t, page, browserToolRunCommand)

	browserAction(t, ctx, browserActionClick, map[string]any{
		browserSelectorKey: "button[aria-pressed]",
	})

	details := browserElement(t, ctx, browserSelectorDetails)
	require.Contains(t, details, browserLiveEventsLabel)
	require.NotContains(t, details, browserLiveEventsLabel+"0)")

	networkLog := browserNetworkLog(t, ctx)
	requireNetworkRequest(t, networkLog, http.MethodGet, browserModelsPath)
	requireNetworkRequest(t, networkLog, http.MethodGet, browserWorkspaceRootsPath)
	requireNetworkRequest(t, networkLog, http.MethodPost, browserSessionsOpenPath)
	requireNetworkRequest(t, networkLog, http.MethodGet, browserMessagesPath)
	requireNetworkRequest(t, networkLog, http.MethodGet, browserWebSocketPath)
	requireNetworkResponse(t, networkLog, browserModelsPath, http.StatusOK)
	requireNetworkResponse(t, networkLog, browserWorkspaceRootsPath, http.StatusOK)
	requireNetworkResponse(t, networkLog, browserSessionsOpenPath, http.StatusOK)
	requireNoNetworkRequest(t, networkLog, http.MethodPost, browserMessagesPath)

	consoleLog := browserConsoleLog(t, ctx)
	requireSafeBrowserDiagnostics(t, consoleLog)
	requireScreenshot(t, ctx)
}

func requireWorkspaceRootAvailable(t *testing.T, ctx context.Context) {
	t.Helper()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		integrationInfra.APIURL(browserWorkspaceRootsPath),
		nil,
	)
	require.NoError(t, err)
	request.Header.Set(
		aichteeteapee.HeaderNameAuthorization,
		"Bearer "+testinfra.TestAPIToken,
	)

	response, err := integrationInfra.HTTPClient().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))

	roots := api.WorkspaceRootList{}
	require.NoError(t, json.Unmarshal(body, &roots))
	require.Contains(t, roots.Roots, browserTestWorkspace)
}

func reportBrowserFailureState(t *testing.T) {
	t.Helper()
	if !t.Failed() {
		return
	}

	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(t.Context()),
		browserRequestTimeout,
	)
	defer cancel()

	if page, err := stealthyBrowser.action(ctx, browserActionGetText, nil); err == nil {
		t.Logf("browser page after failure: %s", page)
	} else {
		t.Logf("read browser page after failure: %v", err)
	}
	if entries, err := stealthyBrowser.action(ctx, browserActionNetworkRead, nil); err == nil {
		t.Logf("browser network after failure: %s", entries)
	} else {
		t.Logf("read browser network after failure: %v", err)
	}
	if entries, err := stealthyBrowser.action(ctx, browserActionConsoleRead, nil); err == nil {
		t.Logf("browser console after failure: %s", entries)
	} else {
		t.Logf("read browser console after failure: %v", err)
	}
}

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

func (c *browserClient) waitForReady(ctx context.Context) error {
	ticker := time.NewTicker(browserPollInterval)
	defer ticker.Stop()

	for {
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			c.baseURL+browserHealthPath,
			nil,
		)
		if err != nil {
			return ctxerrors.Wrap(err, "create Stealthy browser readiness request")
		}

		response, requestErr := c.client.Do(request)
		if requestErr == nil {
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && closeErr == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctxerrors.Wrap(ctx.Err(), "wait for Stealthy browser readiness")
		case <-ticker.C:
		}
	}
}

func (c *browserClient) action(
	ctx context.Context,
	action string,
	parameters map[string]any,
) (json.RawMessage, error) {
	command := map[string]any{browserActionKey: action}
	for key, value := range parameters {
		command[key] = value
	}

	payload, err := json.Marshal(command)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "marshal Stealthy browser command")
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+browserCommandPath,
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create Stealthy browser command request")
	}
	request.Header.Set(aichteeteapee.HeaderNameContentType, aichteeteapee.ContentTypeJSON)

	response, err := c.client.Do(request)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "send Stealthy browser command")
	}

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(
			wrapOptionalError(readErr, "read Stealthy browser command response"),
			wrapOptionalError(closeErr, "close Stealthy browser command response"),
		)
	}

	if response.StatusCode != http.StatusOK {
		return nil, ctxerrors.New("Stealthy browser command returned a non-OK status")
	}

	result := browserResponse{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, ctxerrors.Wrap(err, "decode Stealthy browser command response")
	}
	if !result.Success {
		return nil, ctxerrors.New("Stealthy browser command failed")
	}

	return result.Data, nil
}

func (c *browserClient) screenshot(ctx context.Context) ([]byte, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+browserScreenshotPath,
		nil,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create browser screenshot request")
	}

	response, err := c.client.Do(request)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get browser screenshot")
	}

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(
			wrapOptionalError(readErr, "read browser screenshot"),
			wrapOptionalError(closeErr, "close browser screenshot response"),
		)
	}
	if response.StatusCode != http.StatusOK {
		return nil, ctxerrors.New("browser screenshot returned a non-OK status")
	}

	return body, nil
}

func browserAction(
	t *testing.T,
	ctx context.Context,
	action string,
	parameters map[string]any,
) json.RawMessage {
	t.Helper()

	data, err := stealthyBrowser.action(ctx, action, parameters)
	require.NoErrorf(t, err, "browser action %q failed", action)

	return data
}

func browserText(t *testing.T, ctx context.Context) string {
	t.Helper()

	data := browserAction(t, ctx, browserActionGetText, nil)
	result := browserTextResponse{}
	require.NoError(t, json.Unmarshal(data, &result))

	return result.Text
}

func browserElement(t *testing.T, ctx context.Context, selector string) string {
	t.Helper()

	data := browserAction(t, ctx, browserActionGetElement, map[string]any{
		browserSelectorKey: selector,
	})
	result := browserElementResponse{}
	require.NoError(t, json.Unmarshal(data, &result))

	return result.Text
}

func browserNetworkLog(t *testing.T, ctx context.Context) []browserLog {
	t.Helper()

	data := browserAction(t, ctx, browserActionNetworkRead, nil)
	result := browserLogResponse{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, result.Count, len(result.Log))

	return result.Log
}

func browserConsoleLog(t *testing.T, ctx context.Context) []browserLog {
	t.Helper()

	data := browserAction(t, ctx, browserActionConsoleRead, nil)
	result := browserLogResponse{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, result.Count, len(result.Log))

	return result.Log
}

func assertNoDockerSocketMount(t *testing.T, target testcontainers.Container) {
	t.Helper()

	inspection, err := target.Inspect(t.Context())
	require.NoError(t, err)
	for _, mount := range inspection.Mounts {
		require.NotEqual(t, dockerSocketDestination, mount.Destination)
	}
}

func requireNetworkRequest(
	t *testing.T,
	entries []browserLog,
	method string,
	path string,
) {
	t.Helper()

	for _, entry := range entries {
		if entry.Type == browserNetworkRequest && entry.Method == method &&
			networkPath(entry.URL) == path {
			return
		}
	}

	t.Fatalf("browser did not issue expected %s request to %s", method, path)
}

func requireNetworkResponse(
	t *testing.T,
	entries []browserLog,
	path string,
	status int,
) {
	t.Helper()

	for _, entry := range entries {
		if entry.Type == browserNetworkResponse && entry.Status == status &&
			networkPath(entry.URL) == path {
			return
		}
	}

	t.Fatalf("browser did not receive expected status %d from %s", status, path)
}

func requireNoNetworkRequest(
	t *testing.T,
	entries []browserLog,
	method string,
	path string,
) {
	t.Helper()

	for _, entry := range entries {
		require.Falsef(
			t,
			entry.Type == browserNetworkRequest && entry.Method == method &&
				networkPath(entry.URL) == path,
			"browser unexpectedly issued %s to %s",
			method,
			path,
		)
	}
}

func networkPath(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	return parsed.Path
}

func requireSafeBrowserDiagnostics(t *testing.T, entries []browserLog) {
	t.Helper()

	seenDiagnostics := 0
	seenEvents := make(map[string]bool)
	for _, entry := range entries {
		require.NotContains(t, entry.Text, testinfra.TestAPIToken)
		require.NotContains(t, entry.Text, browserTestMessage)
		require.NotEqual(t, browserConsoleError, entry.Type)

		if !strings.HasPrefix(entry.Text, browserLogPrefix) {
			continue
		}

		seenDiagnostics++
		payload := map[string]any{}
		require.NoError(t, json.Unmarshal(
			[]byte(strings.TrimPrefix(entry.Text, browserLogPrefix)),
			&payload,
		))
		event, ok := payload[browserEventKey].(string)
		require.True(t, ok)
		require.NotEmpty(t, event)
		seenEvents[event] = true

		for key, value := range payload {
			require.NotContains(t, strings.ToLower(key), "token")
			require.NotContains(t, strings.ToLower(key), "message")
			require.NotContains(t, strings.ToLower(key), "body")
			require.NotContains(t, strings.ToLower(key), "content")
			require.NotContains(t, strings.ToLower(key), "data")
			require.NotContains(t, strings.ToLower(key), "error")

			switch value.(type) {
			case bool, float64, nil, string:
			default:
				t.Fatalf("browser diagnostic %q has non-primitive metadata", key)
			}
		}
	}

	require.Greater(t, seenDiagnostics, 0)
	for _, event := range expectedBrowserDiagnostics {
		require.Truef(t, seenEvents[event], "missing browser diagnostic %q", event)
	}
}

func requireScreenshot(t *testing.T, ctx context.Context) {
	t.Helper()

	screenshot, err := stealthyBrowser.screenshot(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, screenshot)

	configuration, err := png.DecodeConfig(bytes.NewReader(screenshot))
	require.NoError(t, err)
	require.Positive(t, configuration.Width)
	require.Positive(t, configuration.Height)
	require.LessOrEqual(t, configuration.Width, browserMaxScreenshotEdge)
	require.LessOrEqual(t, configuration.Height, browserMaxScreenshotEdge)

	artifactDirectory := t.TempDir()
	if configuredRoot := os.Getenv(browserArtifactRootEnv); configuredRoot != "" {
		info, statErr := os.Stat(configuredRoot)
		require.NoError(t, statErr)
		require.True(t, info.IsDir())

		artifactDirectory, err = os.MkdirTemp(configuredRoot, "control-surface-")
		require.NoError(t, err)
	}

	artifactPath := filepath.Join(artifactDirectory, "control-surface.png")
	require.NoError(t, os.WriteFile(artifactPath, screenshot, 0o600))
	t.Logf(
		"browser screenshot path=%s dimensions=%dx%d",
		artifactPath,
		configuration.Width,
		configuration.Height,
	)
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
