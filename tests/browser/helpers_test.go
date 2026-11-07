//go:build browser

package browser_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/psyb0t/aichteeteapee"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
)

// prepareBrowserTest turns on console and network capture, so no test relies
// on another having run first, and reports the browser state on failure.
func prepareBrowserTest(t *testing.T) {
	t.Helper()

	browserAction(t.Context(), t, browserActionConsoleLog, nil)
	browserAction(t.Context(), t, browserActionNetworkLog, nil)
	t.Cleanup(func() {
		reportBrowserFailureState(t)
	})
}

// openBrowserWorkspace loads the control surface from scratch, connects with
// the test token, and opens the workspace below the test root. The workspace
// directory is created first, so opening it never fails on a missing path.
func openBrowserWorkspace(ctx context.Context, t *testing.T, name string) {
	t.Helper()

	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		ctx,
		name+"/"+browserWorkspaceKeepFile,
		[]byte(browserWorkspaceKeepContent),
	))

	browserAction(ctx, t, browserActionGoto, map[string]any{
		"url":               integrationInfra.APIURL("/"),
		browserWaitUntilKey: browserWaitUntilDOM,
	})
	waitForBrowserText(ctx, t, browserMainHeading)
	browserAction(ctx, t, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorToken,
		browserValueKey:    testinfra.TestAPIToken,
	})
	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorConnect,
	})
	browserAction(ctx, t, browserActionWaitForElement, map[string]any{
		browserSelectorKey: browserSelectorSocketOpen,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})
	browserAction(ctx, t, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorWorkspace,
		browserValueKey:    browserTestWorkspace + "/" + name,
	})
	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorOpen,
	})
	waitForBrowserText(ctx, t, browserModelReference)
}

func sendBrowserMessage(ctx context.Context, t *testing.T, text string) {
	t.Helper()

	browserAction(ctx, t, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorMessage,
		browserValueKey:    text,
	})
	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorSend,
	})
}

func waitForBrowserText(ctx context.Context, t *testing.T, text string) {
	t.Helper()

	browserAction(ctx, t, browserActionWaitForText, map[string]any{
		browserTextKey:    text,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})
}

func waitForBrowserElement(ctx context.Context, t *testing.T, selector string) {
	t.Helper()

	browserAction(ctx, t, browserActionWaitForElement, map[string]any{
		browserSelectorKey: selector,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})
}

func clickBrowserElement(ctx context.Context, t *testing.T, selector string) {
	t.Helper()

	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: selector,
	})
}

func holdNextProviderCompletion(t *testing.T) *testinfra.CompletionHold {
	t.Helper()

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	return hold
}

func awaitProviderCompletionHold(t *testing.T, hold *testinfra.CompletionHold) {
	t.Helper()

	select {
	case <-hold.Observed:
	case <-time.After(browserRequestTimeout):
		t.Fatal("the provider never received the held completion request")
	}
}

func openBrowserAPISession(ctx context.Context, t *testing.T, workspace string) string {
	t.Helper()

	body, err := json.Marshal(map[string]string{"workspace": workspace})
	require.NoError(t, err)

	response := browserAPIRequest(
		ctx,
		t,
		browserSessionsOpenPath,
		body,
		"",
	)
	require.Equal(t, http.StatusOK, response.status, string(response.body))

	opened := api.OpenedSession{}
	require.NoError(t, json.Unmarshal(response.body, &opened))

	return opened.Session.Id.String()
}

func postBrowserNotice(ctx context.Context, t *testing.T, sessionID string) {
	t.Helper()

	response := browserAPIRequest(
		ctx,
		t,
		browserSessionNoticesPath,
		[]byte(browserNoticeBody),
		sessionID,
	)
	require.Equal(t, http.StatusAccepted, response.status, string(response.body))
}

func requireWorkspaceRootAvailable(ctx context.Context, t *testing.T) {
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
	defer func() { _ = response.Body.Close() }()

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
	// The browser shows only a generic message for a server failure, so the
	// controller's own log says what went wrong.
	if logs, err := integrationInfra.AppLogs(ctx); err == nil {
		t.Logf("controller logs after failure: %s", logs)
	} else {
		t.Logf("read controller logs after failure: %v", err)
	}
}
