//go:build browser

package browser_test

import (
	"fmt"
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
)

// The peen command opens a workspace over the API and prints a link that names
// the session. Loading that link and connecting shows that session's chat with
// no click on the session list.
func TestControlSurfaceOpensTheSessionALinkNames(t *testing.T) {
	ctx := t.Context()
	prepareBrowserTest(t)

	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		ctx,
		browserLinkWorkspaceName+"/"+browserWorkspaceKeepFile,
		[]byte(browserWorkspaceKeepContent),
	))
	sessionID := openBrowserAPISession(
		ctx,
		t,
		browserTestWorkspace+"/"+browserLinkWorkspaceName,
	)

	browserAction(ctx, t, browserActionGoto, map[string]any{
		"url": integrationInfra.APIURL(
			fmt.Sprintf(browserSessionLinkFormat, sessionID),
		),
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

	waitForBrowserText(ctx, t, browserModelReference)
	require.Contains(
		t,
		browserElement(ctx, t, browserSelectorChatHeader),
		browserLinkWorkspaceName,
		"the linked session's chat must be open after connecting",
	)
	requireNamedScreenshot(ctx, t, browserLinkScreenshot)
}
