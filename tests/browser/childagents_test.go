//go:build browser

package browser_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
)

// A child that outlives its turn shows in a running-agents panel with the
// question it waits on. Its Stop button cancels it through the API, and the
// panel goes away once it ends.
func TestControlSurfaceListsAWaitingChildAndStopsIt(t *testing.T) {
	ctx := t.Context()
	prepareBrowserTest(t)

	openBrowserWorkspace(ctx, t, browserAgentsWorkspaceName)

	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: browserAgentsMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name: "launchAgent",
			Arguments: map[string]any{
				"agent": browserAgentsCoreAgent,
				"task":  browserAgentsTask,
			},
		}},
		FinalAnswer: browserAgentsRootAnswer,
		Children: []testinfra.ScriptedToolTurn{{
			UserMessage: browserAgentsTask,
			Calls: []testinfra.ScriptedToolCall{{
				Name:      "ask",
				Arguments: map[string]any{"question": browserAgentsQuestion},
			}},
			FinalAnswer: browserAgentsRootAnswer,
		}},
	})
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)

	sendBrowserMessage(ctx, t, browserAgentsMessage)
	waitForBrowserText(ctx, t, browserAgentsRootAnswer)
	waitForBrowserText(ctx, t, browserAgentsAskPrefix+browserAgentsQuestion)
	require.Contains(
		t,
		strings.ToLower(browserText(ctx, t)),
		browserAgentsPanelLabel,
	)
	requireNamedScreenshot(ctx, t, browserAgentsScreenshot)

	clickBrowserElement(ctx, t, browserSelectorStopAgent)

	require.Eventually(t, func() bool {
		return !strings.Contains(
			strings.ToLower(browserText(ctx, t)),
			browserAgentsPanelLabel,
		)
	}, browserRequestTimeout, browserAgentsPollEvery)

	isCancelCalled := false

	for _, entry := range browserNetworkLog(ctx, t) {
		path := networkPath(entry.URL)
		if entry.Method == http.MethodPost &&
			strings.HasPrefix(path, browserAgentsCancelPrefix) &&
			strings.HasSuffix(path, browserAgentsCancelSuffix) {
			isCancelCalled = true
		}
	}

	require.True(t, isCancelCalled, "the Stop button never called the cancel API")
}
