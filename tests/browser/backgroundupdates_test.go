//go:build browser

package browser_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A turn an event handler started has no typed prompt. The chat shows the
// handler's text as a background update, never the raw handler tags, both
// while the turn runs and after a reload.
func TestControlSurfaceShowsAHandlerStartedTurnAsABackgroundUpdate(t *testing.T) {
	ctx := t.Context()
	prepareBrowserTest(t)

	workspace := browserTestWorkspace + "/" + browserHandlerWorkspaceName
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		ctx,
		browserHandlerWorkspaceName+"/"+browserHandlerEventFile,
		[]byte(browserHandlerFile),
	))
	openBrowserWorkspace(ctx, t, browserHandlerWorkspaceName)
	sessionID := openBrowserAPISession(ctx, t, workspace)

	hold := holdNextProviderCompletion(t)
	postBrowserNotice(ctx, t, sessionID)
	awaitProviderCompletionHold(t, hold)

	waitForBrowserText(ctx, t, browserUpdateLabel)
	requireHandlerUpdateWithoutRawTags(ctx, t, browserHandlerPosition)
	requireNamedScreenshot(ctx, t, browserHandlerScreenshot)

	hold.Release()
	waitForBrowserText(ctx, t, browserCompletionPrefix)

	openBrowserWorkspace(ctx, t, browserHandlerWorkspaceName)
	waitForBrowserText(ctx, t, browserUpdateLabel)
	requireHandlerUpdateWithoutRawTags(ctx, t, browserHandlerPosition)
	requireEventsUpdateReadable(ctx, t, browserEventsPosition)
}

// requireHandlerUpdateWithoutRawTags opens the handler's background update,
// which is the position-th block of the reply, and checks it shows exactly the
// event type, the instructions, and the agent sentence, while the page never
// shows the handler tags. The handler's text is the first block of the reply
// while the turn is live and after a reload, because the turn stores it before
// the events block the model receives after it.
func requireHandlerUpdateWithoutRawTags(
	ctx context.Context,
	t *testing.T,
	position int,
) {
	t.Helper()

	update := fmt.Sprintf(browserSelectorUpdateAt, position)
	waitForBrowserElement(ctx, t, update+browserSelectorDetailsClosed)
	clickBrowserElement(ctx, t, update+" summary")
	waitForBrowserElement(ctx, t, update+browserSelectorDetailsOpen)

	require.Equal(
		t,
		browserHandlerUpdateText,
		strings.TrimSpace(browserElement(ctx, t, update)),
	)

	page := browserText(ctx, t)
	require.NotContains(t, page, browserRawHandlerOpen)
	require.NotContains(t, page, browserRawHandlerClose)
	require.NotContains(t, page, browserRawAgentOpen)
}

// requireEventsUpdateReadable opens the delivered events block, which is the
// position-th block of the reply, and checks it lists the event as its type
// and summary while the page never shows the events tags or the event JSON.
func requireEventsUpdateReadable(
	ctx context.Context,
	t *testing.T,
	position int,
) {
	t.Helper()

	update := fmt.Sprintf(browserSelectorUpdateAt, position)
	waitForBrowserElement(ctx, t, update+browserSelectorDetailsClosed)
	clickBrowserElement(ctx, t, update+" summary")
	waitForBrowserElement(ctx, t, update+browserSelectorDetailsOpen)

	require.Equal(
		t,
		browserEventsUpdateText,
		strings.TrimSpace(browserElement(ctx, t, update)),
	)

	page := browserText(ctx, t)
	require.NotContains(t, page, browserRawEventsOpen)
	require.NotContains(t, page, browserRawEventsClose)
	require.NotContains(t, page, browserRawEventOpen)
	require.NotContains(t, page, browserRawEventClose)
	require.NotContains(t, page, browserRawEventJSONKey)
}
