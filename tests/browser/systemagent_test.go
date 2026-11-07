//go:build browser

package browser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	browserSystemWorkspaceName = "browser-system-agent"
	browserSystemCommand       = `/system-agent "report the deployment"`
	browserSystemFollowUp      = "and now report the workers"
	browserSystemLabel         = "System agent"
	browserSystemScreenshot    = "system-agent"
	browserSystemMismatch      = "does not match the session"
)

// The composer's command opens the system session, and a plain follow-up has
// to reach it too. The controller refuses a follow-up without the system field.
func TestControlSurfaceRunsTheSystemAgent(t *testing.T) {
	ctx := t.Context()
	prepareBrowserTest(t)

	openBrowserWorkspace(ctx, t, browserSystemWorkspaceName)
	sendBrowserMessage(ctx, t, browserSystemCommand)
	waitForBrowserText(ctx, t, browserSystemLabel)
	waitForBrowserText(ctx, t, browserCompletionPrefix)

	sendBrowserMessage(ctx, t, browserSystemFollowUp)
	waitForBrowserText(ctx, t, browserSystemFollowUp)

	assert.NotContains(t, browserText(ctx, t), browserSystemMismatch)
	requireNamedScreenshot(ctx, t, browserSystemScreenshot)
}
