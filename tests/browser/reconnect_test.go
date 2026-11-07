//go:build browser

package browser_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A dropped WebSocket mid-turn recovers on its own: the control surface
// reconnects with no explicit retry from the person using it, catches up
// through the session feed, and the turn's final reply renders exactly
// once, with no page reload.
func TestControlSurfaceRecoversADroppedSocketMidTurn(t *testing.T) {
	ctx := t.Context()
	prepareBrowserTest(t)

	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		ctx,
		browserReconnectWorkspaceName+"/"+browserWorkspaceKeepFile,
		[]byte(browserWorkspaceKeepContent),
	))

	browserAction(ctx, t, browserActionGoto, map[string]any{
		"url":               integrationInfra.APIURL("/"),
		browserWaitUntilKey: browserWaitUntilDOM,
	})
	waitForBrowserText(ctx, t, browserMainHeading)
	// Installed before Connect is clicked, so the hook is in place before the
	// page ever opens its one global WebSocket.
	installBrowserSocketCapture(ctx, t)

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
		browserValueKey: browserTestWorkspace + "/" +
			browserReconnectWorkspaceName,
	})
	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorOpen,
	})
	waitForBrowserText(ctx, t, browserModelReference)

	hold := holdNextProviderCompletion(t)
	sendBrowserMessage(ctx, t, browserReconnectMessage)
	awaitProviderCompletionHold(t, hold)

	dropBrowserWebSocket(ctx, t)

	browserAction(ctx, t, browserActionWaitForElement, map[string]any{
		browserSelectorKey: browserSelectorSocketOpen,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})

	hold.Release()
	waitForBrowserText(ctx, t, browserCompletionPrefix)

	page := browserText(ctx, t)
	require.Contains(t, page, browserReconnectMessage)
	assert.Equal(
		t,
		1,
		strings.Count(page, browserCompletionPrefix),
		"the final reply must render exactly once after a reconnect",
	)

	requireNamedScreenshot(ctx, t, browserReconnectScreenshot)
}

// installBrowserSocketCapture wraps the page's global WebSocket constructor
// so a later dropBrowserWebSocket call can reach the instance the control
// surface opened, which it otherwise keeps private.
func installBrowserSocketCapture(ctx context.Context, t *testing.T) {
	t.Helper()

	result := browserEvalStringResult(ctx, t, browserEvalInstallSocketCapture)
	require.Contains(t, []string{"installed", "already-installed"}, result)
}

// dropBrowserWebSocket closes the most recently captured WebSocket from the
// page's own side, simulating a dropped connection the control surface did
// not ask for.
func dropBrowserWebSocket(ctx context.Context, t *testing.T) {
	t.Helper()

	data := browserAction(ctx, t, browserActionEval, map[string]any{
		browserExpressionKey: browserEvalDropLastSocket,
	})

	result := struct {
		Result bool `json:"result"`
	}{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.True(t, result.Result, "the page had no captured WebSocket to drop")
}
