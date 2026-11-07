//go:build browser

package browser_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A skill named in a message is stored as its own message. After a reload the
// chat shows it as a collapsed "skill: name" marker that opens on a click.
func TestControlSurfaceShowsAStoredSkillMessageAsAMarker(t *testing.T) {
	ctx := t.Context()
	prepareBrowserTest(t)

	openBrowserWorkspace(ctx, t, browserSkillWorkspaceName)
	sendBrowserMessage(ctx, t, browserSkillMessage)
	waitForBrowserText(ctx, t, browserCompletionPrefix)

	// A reload drops every live block, so the chat can only render the
	// stored transcript.
	openBrowserWorkspace(ctx, t, browserSkillWorkspaceName)
	waitForBrowserText(ctx, t, browserSkillMarkerLabel)
	requireSkillMarkerExpandsOnClick(ctx, t, "")
	requireNamedScreenshot(ctx, t, browserSkillScreenshot)
}

// The skill message reaches the chat while the turn is still running, and the
// skill.injected event behind it is not listed as activity.
func TestControlSurfaceShowsALiveSkillMessageDuringATurn(t *testing.T) {
	ctx := t.Context()
	prepareBrowserTest(t)

	openBrowserWorkspace(ctx, t, browserLiveSkillWorkspaceName)
	hold := holdNextProviderCompletion(t)
	sendBrowserMessage(ctx, t, browserLiveSkillMessage)
	awaitProviderCompletionHold(t, hold)

	browserAction(ctx, t, browserActionWaitForElement, map[string]any{
		browserSelectorKey: browserSelectorLiveReply + browserSelectorSkillMarker,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})
	require.Contains(
		t,
		browserText(ctx, t),
		browserWorkingLabel,
		"the skill marker must appear while the turn is still running",
	)
	requireSkillMarkerExpandsOnClick(ctx, t, browserSelectorLiveReply)
	requireNamedScreenshot(ctx, t, browserLiveSkillScreenshot)

	hold.Release()
	waitForBrowserText(ctx, t, browserCompletionPrefix)
	requireSkillInjectedOnlyInRawEvents(ctx, t)
}

// requireSkillMarkerExpandsOnClick checks that the one skill marker inside
// scope starts closed with only its label showing, then shows the skill
// content once it is clicked.
func requireSkillMarkerExpandsOnClick(ctx context.Context, t *testing.T, scope string) {
	t.Helper()

	marker := scope + browserSelectorSkillMarker
	waitForBrowserElement(ctx, t, marker+browserSelectorDetailsClosed)

	collapsed := browserElement(ctx, t, marker)
	require.Equal(t, browserSkillMarkerLabel, strings.TrimSpace(collapsed))

	clickBrowserElement(ctx, t, marker+" summary")
	waitForBrowserElement(ctx, t, marker+browserSelectorDetailsOpen)

	expanded := browserElement(ctx, t, marker)
	require.Contains(t, expanded, browserSkillMarkerLabel)
	require.Contains(t, expanded, browserSkillNameTag)
	require.Contains(t, expanded, browserSkillCloseTag)
}

// requireSkillInjectedOnlyInRawEvents checks that the chat lists no activity
// for skill.injected, while the event itself did reach the browser and shows in
// the inspector's raw event list.
func requireSkillInjectedOnlyInRawEvents(ctx context.Context, t *testing.T) {
	t.Helper()

	require.NotContains(
		t,
		browserText(ctx, t),
		browserSkillInjectedEvent,
		"skill.injected must not be listed as activity",
	)

	clickBrowserElement(ctx, t, browserSelectorDetailsToggle)
	clickBrowserElement(ctx, t, browserSelectorLiveEventsSummary)
	rawEvents := browserElement(ctx, t, browserSelectorInspectorLiveEvents)
	require.Contains(t, rawEvents, browserLiveEventsLabel)
	require.Contains(
		t,
		rawEvents,
		browserSkillInjectedEvent,
		"the browser must have received the event for the check above to mean anything",
	)
}
