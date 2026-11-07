//go:build browser

package browser_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
)

func TestControlSurfaceCompletesATurnInARealBrowser(t *testing.T) {
	assertNoDockerSocketMount(t, browserContainer)

	ctx := t.Context()
	requireWorkspaceRootAvailable(ctx, t)
	prepareBrowserTest(t)
	browserAction(ctx, t, browserActionGoto, map[string]any{
		"url":               integrationInfra.APIURL("/"),
		browserWaitUntilKey: browserWaitUntilDOM,
	})
	browserAction(ctx, t, browserActionWaitForText, map[string]any{
		browserTextKey:    browserMainHeading,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})

	browserAction(ctx, t, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorToken,
		browserValueKey:    testinfra.TestAPIToken,
	})
	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorConnect,
	})
	browserAction(ctx, t, browserActionWaitForElement, map[string]any{
		browserSelectorKey: browserSelectorOpen,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})
	browserAction(ctx, t, browserActionWaitForElement, map[string]any{
		browserSelectorKey: browserSelectorSocketOpen,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		ctx,
		browserBrokenSkillFixture,
		[]byte(browserBrokenSkillContent),
	))
	browserAction(ctx, t, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorWorkspace,
		browserValueKey:    browserChildWorkspace,
	})
	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorOpen,
	})
	browserAction(ctx, t, browserActionWaitForText, map[string]any{
		browserTextKey:    browserModelReference,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})

	browserAction(ctx, t, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorMessage,
		browserValueKey:    browserThinkingMessage,
	})
	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorSend,
	})
	browserAction(ctx, t, browserActionWaitForText, map[string]any{
		browserTextKey:    browserCompletionPrefix,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})
	browserAction(ctx, t, browserActionWaitForText, map[string]any{
		browserTextKey:    browserHarnessWarningLabel,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})
	browserAction(ctx, t, browserActionWaitForText, map[string]any{
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

	browserAction(ctx, t, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorMessage,
		browserValueKey:    browserTestMessage,
	})
	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorSend,
	})
	browserAction(ctx, t, browserActionWaitForText, map[string]any{
		browserTextKey:    browserCompletionText,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})
	require.Eventually(t, func() bool {
		return browserEvalStringResult(
			ctx,
			t,
			browserEvalLiveTurnGone,
		) == browserEvalTrue
	}, browserRequestTimeout, browserTranscriptPollEvery)
	// CSS upper-cases the status labels, so card text is compared without case.
	doneCard := strings.ToLower(
		browserElement(ctx, t, browserSelectorToolCardDone),
	)
	require.Contains(t, doneCard, browserToolStatusDone)

	errorCard := browserElement(ctx, t, browserSelectorToolCardError)
	require.Contains(t, errorCard, browserToolApplyPatch)
	require.Contains(t, strings.ToLower(errorCard), browserToolStatusError)

	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorToolCardErrorSummary,
	})
	expandedErrorCard := strings.ToLower(
		browserElement(ctx, t, browserSelectorToolCardError),
	)
	require.Contains(t, expandedErrorCard, browserToolArgumentsLabel)

	page := browserText(ctx, t)
	require.NotContains(t, page, browserOrphanToolResultName)
	require.Contains(t, page, browserThinkingMessage)
	require.Contains(t, page, browserCompletionPrefix)
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
	require.NotContains(
		t,
		page,
		browserStreamProtocolEvent,
		"streamed content blocks must fold into the reply, not render as rows",
	)

	browserAction(ctx, t, browserActionClick, map[string]any{
		browserSelectorKey: "button[aria-pressed]",
	})

	details := browserElement(ctx, t, browserSelectorDetails)
	require.Contains(t, details, browserLiveEventsLabel)
	require.NotContains(t, details, browserLiveEventsLabel+"0)")

	networkLog := browserNetworkLog(ctx, t)
	requireNetworkRequest(t, networkLog, http.MethodGet, browserModelsPath)
	requireNetworkRequest(t, networkLog, http.MethodGet, browserWorkspaceRootsPath)
	requireNetworkRequest(t, networkLog, http.MethodPost, browserSessionsOpenPath)
	requireNetworkRequest(t, networkLog, http.MethodGet, browserMessagesPath)
	requireNetworkRequest(t, networkLog, http.MethodGet, browserWebSocketPath)
	requireNetworkResponse(t, networkLog, browserModelsPath, http.StatusOK)
	requireNetworkResponse(t, networkLog, browserWorkspaceRootsPath, http.StatusOK)
	requireNetworkResponse(t, networkLog, browserSessionsOpenPath, http.StatusOK)
	requireNoNetworkRequest(t, networkLog, http.MethodPost, browserMessagesPath)

	consoleLog := browserConsoleLog(ctx, t)
	requireSafeBrowserDiagnostics(t, consoleLog)
	requireScreenshot(ctx, t)
}
