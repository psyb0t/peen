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
	browserSelectorToolCardDone          = `details.tool-card[data-status="done"]`
	browserSelectorToolCardError         = `details.tool-card[data-status="error"]`
	browserSelectorToolCardErrorSummary  = browserSelectorToolCardError + " summary"
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
	browserToolStatusDone                = "done"
	browserToolStatusError               = "error"
	browserToolArgumentsLabel            = "arguments"
	browserOrphanToolResultName          = "Tool result"
	browserLiveEventsLabel               = "Live events ("
	browserStreamProtocolEvent           = "content_block_"
	browserModelReference                = "integration/test-model"
	browserMaxScreenshotEdge             = 512
	browserImage                         = "psyb0t/stealthy-auto-browse@sha256:60ed29e9775b9af7d3a457f5d9d3fe0039f4cfe2fc98a405eedcb286dc44bbe1"
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

const (
	browserSelectorSkillMarker         = "details.note-card.skill"
	browserSelectorDetailsClosed       = ":not([open])"
	browserSelectorDetailsOpen         = "[open]"
	browserSelectorLiveReply           = "article.message.assistant.live "
	browserSelectorLiveEventsSummary   = "aside.inspector details:nth-of-type(2) summary"
	browserSelectorInspectorLiveEvents = "aside.inspector details:nth-of-type(2)"
	browserSelectorDetailsToggle       = "button[aria-pressed]"

	browserSkillWorkspaceName     = "browser-skill-stored"
	browserLiveSkillWorkspaceName = "browser-skill-live"
	browserHandlerWorkspaceName   = "browser-handler-wake"
	browserWorkspaceKeepFile      = ".keep"
	browserWorkspaceKeepContent   = "keep\n"
	browserSkillMessage           = "plan this with :core:planning"
	browserLiveSkillMessage       = "plan this live with :core:planning"
	browserSkillMarkerLabel       = "skill: core:planning"
	browserSkillNameTag           = "<name>core:planning</name>"
	browserSkillCloseTag          = "</skill>"
	browserSkillInjectedEvent     = "skill.injected"
	browserCompletionPrefix       = "integration completion"
	browserWorkingLabel           = "Working"
	browserUpdateLabel            = "Background update"
	browserHandlerEventType       = "browser.wake"
	browserHandlerEventFile       = ".agents/events/" + browserHandlerEventType + ".md"
	browserHandlerAgent           = "core:general-purpose"
	browserHandlerInstructions    = "Report that the browser wake handler ran."
	browserHandlerFile            = "---\ntype: " + browserHandlerEventType +
		"\ndelivery: wake\nagent: " + browserHandlerAgent + "\n---\n" +
		browserHandlerInstructions
	browserHandlerUpdateText = browserUpdateLabel + "\nEvent handler for " +
		browserHandlerEventType + "\n\n" + browserHandlerInstructions + "\n\n" +
		"Handle this as the " + browserHandlerAgent + " agent would."
	browserRawHandlerOpen  = "<handler>"
	browserRawHandlerClose = "</handler>"
	browserRawAgentOpen    = "<agent>"
	browserNoticeSummary   = "an outside system needs the agent"
	browserNoticeBody      = `{"type":"` + browserHandlerEventType +
		`","summary":"` + browserNoticeSummary + `"}`
	browserEventsUpdateText = browserUpdateLabel + "\nSession events\n\n" +
		browserHandlerEventType + ": " + browserNoticeSummary
	browserRawEventsOpen       = "<events>"
	browserRawEventsClose      = "</events>"
	browserRawEventOpen        = "<event>"
	browserRawEventClose       = "</event>"
	browserRawEventJSONKey     = `"summary"`
	browserSelectorUpdateAt    = ".reply-blocks > details.note-card.injected:nth-child(%d)"
	browserHandlerPosition     = 1
	browserEventsPosition      = 2
	browserSessionIDHeader     = "X-Session-ID"
	browserSessionNoticesPath  = "/v1/session/notices"
	browserBearerPrefix        = "Bearer "
	browserScreenshotBaseName  = "control-surface"
	browserSkillScreenshot     = "skill-marker"
	browserLiveSkillScreenshot = "live-skill-marker"
	browserHandlerScreenshot   = "handler-update"
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
	// CSS upper-cases the status labels, so card text is compared without case.
	doneCard := strings.ToLower(
		browserElement(t, ctx, browserSelectorToolCardDone),
	)
	require.Contains(t, doneCard, browserToolStatusDone)

	errorCard := strings.ToLower(
		browserElement(t, ctx, browserSelectorToolCardError),
	)
	require.Contains(t, errorCard, browserToolApplyPatch)
	require.Contains(t, errorCard, browserToolStatusError)

	browserAction(t, ctx, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorToolCardErrorSummary,
	})
	expandedErrorCard := strings.ToLower(
		browserElement(t, ctx, browserSelectorToolCardError),
	)
	require.Contains(t, expandedErrorCard, browserToolArgumentsLabel)

	page := browserText(t, ctx)
	require.NotContains(t, page, browserOrphanToolResultName)
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
	require.NotContains(
		t,
		page,
		browserStreamProtocolEvent,
		"streamed content blocks must fold into the reply, not render as rows",
	)

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

// A skill named in a message is stored as its own message. After a reload the
// chat shows it as a collapsed "skill: name" marker that opens on a click.
func TestControlSurfaceShowsAStoredSkillMessageAsAMarker(t *testing.T) {
	ctx := t.Context()
	prepareBrowserTest(t)

	openBrowserWorkspace(t, ctx, browserSkillWorkspaceName)
	sendBrowserMessage(t, ctx, browserSkillMessage)
	waitForBrowserText(t, ctx, browserCompletionPrefix)

	// A reload drops every live block, so the chat can only render the
	// stored transcript.
	openBrowserWorkspace(t, ctx, browserSkillWorkspaceName)
	waitForBrowserText(t, ctx, browserSkillMarkerLabel)
	requireSkillMarkerExpandsOnClick(t, ctx, "")
	requireNamedScreenshot(t, ctx, browserSkillScreenshot)
}

// The skill message reaches the chat while the turn is still running, and the
// skill.injected event behind it is not listed as activity.
func TestControlSurfaceShowsALiveSkillMessageDuringATurn(t *testing.T) {
	ctx := t.Context()
	prepareBrowserTest(t)

	openBrowserWorkspace(t, ctx, browserLiveSkillWorkspaceName)
	hold := holdNextProviderCompletion(t)
	sendBrowserMessage(t, ctx, browserLiveSkillMessage)
	awaitProviderCompletionHold(t, hold)

	browserAction(t, ctx, browserActionWaitForElement, map[string]any{
		browserSelectorKey: browserSelectorLiveReply + browserSelectorSkillMarker,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})
	require.Contains(
		t,
		browserText(t, ctx),
		browserWorkingLabel,
		"the skill marker must appear while the turn is still running",
	)
	requireSkillMarkerExpandsOnClick(t, ctx, browserSelectorLiveReply)
	requireNamedScreenshot(t, ctx, browserLiveSkillScreenshot)

	hold.Release()
	waitForBrowserText(t, ctx, browserCompletionPrefix)
	requireSkillInjectedOnlyInRawEvents(t, ctx)
}

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
	openBrowserWorkspace(t, ctx, browserHandlerWorkspaceName)
	sessionID := openBrowserAPISession(t, ctx, workspace)

	hold := holdNextProviderCompletion(t)
	postBrowserNotice(t, ctx, sessionID)
	awaitProviderCompletionHold(t, hold)

	waitForBrowserText(t, ctx, browserUpdateLabel)
	requireHandlerUpdateWithoutRawTags(t, ctx, browserHandlerPosition)
	requireNamedScreenshot(t, ctx, browserHandlerScreenshot)

	hold.Release()
	waitForBrowserText(t, ctx, browserCompletionPrefix)

	openBrowserWorkspace(t, ctx, browserHandlerWorkspaceName)
	waitForBrowserText(t, ctx, browserUpdateLabel)
	requireHandlerUpdateWithoutRawTags(t, ctx, browserHandlerPosition)
	requireEventsUpdateReadable(t, ctx, browserEventsPosition)
}

func prepareBrowserTest(t *testing.T) {
	t.Helper()

	t.Cleanup(func() {
		reportBrowserFailureState(t)
	})
}

// openBrowserWorkspace loads the control surface from scratch, connects with
// the test token, and opens the workspace below the test root. The workspace
// directory is created first, so opening it never fails on a missing path.
func openBrowserWorkspace(t *testing.T, ctx context.Context, name string) {
	t.Helper()

	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		ctx,
		name+"/"+browserWorkspaceKeepFile,
		[]byte(browserWorkspaceKeepContent),
	))

	browserAction(t, ctx, browserActionGoto, map[string]any{
		"url":               integrationInfra.APIURL("/"),
		browserWaitUntilKey: browserWaitUntilDOM,
	})
	waitForBrowserText(t, ctx, browserMainHeading)
	browserAction(t, ctx, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorToken,
		browserValueKey:    testinfra.TestAPIToken,
	})
	browserAction(t, ctx, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorConnect,
	})
	browserAction(t, ctx, browserActionWaitForElement, map[string]any{
		browserSelectorKey: browserSelectorSocketOpen,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})
	browserAction(t, ctx, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorWorkspace,
		browserValueKey:    browserTestWorkspace + "/" + name,
	})
	browserAction(t, ctx, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorOpen,
	})
	waitForBrowserText(t, ctx, browserModelReference)
}

func sendBrowserMessage(t *testing.T, ctx context.Context, text string) {
	t.Helper()

	browserAction(t, ctx, browserActionFill, map[string]any{
		browserSelectorKey: browserSelectorMessage,
		browserValueKey:    text,
	})
	browserAction(t, ctx, browserActionClick, map[string]any{
		browserSelectorKey: browserSelectorSend,
	})
}

func waitForBrowserText(t *testing.T, ctx context.Context, text string) {
	t.Helper()

	browserAction(t, ctx, browserActionWaitForText, map[string]any{
		browserTextKey:    text,
		browserTimeoutKey: browserRequestTimeout.Seconds(),
	})
}

func waitForBrowserElement(t *testing.T, ctx context.Context, selector string) {
	t.Helper()

	browserAction(t, ctx, browserActionWaitForElement, map[string]any{
		browserSelectorKey: selector,
		browserTimeoutKey:  browserRequestTimeout.Seconds(),
	})
}

func clickBrowserElement(t *testing.T, ctx context.Context, selector string) {
	t.Helper()

	browserAction(t, ctx, browserActionClick, map[string]any{
		browserSelectorKey: selector,
	})
}

// requireSkillMarkerExpandsOnClick checks that the one skill marker inside
// scope starts closed with only its label showing, then shows the skill
// content once it is clicked.
func requireSkillMarkerExpandsOnClick(t *testing.T, ctx context.Context, scope string) {
	t.Helper()

	marker := scope + browserSelectorSkillMarker
	waitForBrowserElement(t, ctx, marker+browserSelectorDetailsClosed)

	collapsed := browserElement(t, ctx, marker)
	require.Equal(t, browserSkillMarkerLabel, strings.TrimSpace(collapsed))

	clickBrowserElement(t, ctx, marker+" summary")
	waitForBrowserElement(t, ctx, marker+browserSelectorDetailsOpen)

	expanded := browserElement(t, ctx, marker)
	require.Contains(t, expanded, browserSkillMarkerLabel)
	require.Contains(t, expanded, browserSkillNameTag)
	require.Contains(t, expanded, browserSkillCloseTag)
}

// requireSkillInjectedOnlyInRawEvents checks that the chat lists no activity
// for skill.injected, while the event itself did reach the browser and shows in
// the inspector's raw event list.
func requireSkillInjectedOnlyInRawEvents(t *testing.T, ctx context.Context) {
	t.Helper()

	require.NotContains(
		t,
		browserText(t, ctx),
		browserSkillInjectedEvent,
		"skill.injected must not be listed as activity",
	)

	clickBrowserElement(t, ctx, browserSelectorDetailsToggle)
	clickBrowserElement(t, ctx, browserSelectorLiveEventsSummary)
	rawEvents := browserElement(t, ctx, browserSelectorInspectorLiveEvents)
	require.Contains(t, rawEvents, browserLiveEventsLabel)
	require.Contains(
		t,
		rawEvents,
		browserSkillInjectedEvent,
		"the browser must have received the event for the check above to mean anything",
	)
}

// requireHandlerUpdateWithoutRawTags opens the handler's background update,
// which is the position-th block of the reply, and checks it shows exactly the
// event type, the instructions, and the agent sentence, while the page never
// shows the handler tags. The handler's text is the first block of the reply
// while the turn is live and after a reload, because the turn stores it before
// the events block the model receives after it.
func requireHandlerUpdateWithoutRawTags(
	t *testing.T,
	ctx context.Context,
	position int,
) {
	t.Helper()

	update := fmt.Sprintf(browserSelectorUpdateAt, position)
	waitForBrowserElement(t, ctx, update+browserSelectorDetailsClosed)
	clickBrowserElement(t, ctx, update+" summary")
	waitForBrowserElement(t, ctx, update+browserSelectorDetailsOpen)

	require.Equal(
		t,
		browserHandlerUpdateText,
		strings.TrimSpace(browserElement(t, ctx, update)),
	)

	page := browserText(t, ctx)
	require.NotContains(t, page, browserRawHandlerOpen)
	require.NotContains(t, page, browserRawHandlerClose)
	require.NotContains(t, page, browserRawAgentOpen)
}

// requireEventsUpdateReadable opens the delivered events block, which is the
// position-th block of the reply, and checks it lists the event as its type
// and summary while the page never shows the events tags or the event JSON.
func requireEventsUpdateReadable(
	t *testing.T,
	ctx context.Context,
	position int,
) {
	t.Helper()

	update := fmt.Sprintf(browserSelectorUpdateAt, position)
	waitForBrowserElement(t, ctx, update+browserSelectorDetailsClosed)
	clickBrowserElement(t, ctx, update+" summary")
	waitForBrowserElement(t, ctx, update+browserSelectorDetailsOpen)

	require.Equal(
		t,
		browserEventsUpdateText,
		strings.TrimSpace(browserElement(t, ctx, update)),
	)

	page := browserText(t, ctx)
	require.NotContains(t, page, browserRawEventsOpen)
	require.NotContains(t, page, browserRawEventsClose)
	require.NotContains(t, page, browserRawEventOpen)
	require.NotContains(t, page, browserRawEventClose)
	require.NotContains(t, page, browserRawEventJSONKey)
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

func openBrowserAPISession(t *testing.T, ctx context.Context, workspace string) string {
	t.Helper()

	body, err := json.Marshal(map[string]string{"workspace": workspace})
	require.NoError(t, err)

	response := browserAPIRequest(
		t,
		ctx,
		browserSessionsOpenPath,
		body,
		"",
	)
	require.Equal(t, http.StatusOK, response.status, string(response.body))

	opened := api.OpenedSession{}
	require.NoError(t, json.Unmarshal(response.body, &opened))

	return opened.Session.Id.String()
}

func postBrowserNotice(t *testing.T, ctx context.Context, sessionID string) {
	t.Helper()

	response := browserAPIRequest(
		t,
		ctx,
		browserSessionNoticesPath,
		[]byte(browserNoticeBody),
		sessionID,
	)
	require.Equal(t, http.StatusAccepted, response.status, string(response.body))
}

type browserAPIResponse struct {
	status int
	body   []byte
}

// browserAPIRequest posts one JSON body to the controller as the test token. A
// non-empty sessionID is sent as the session header.
func browserAPIRequest(
	t *testing.T,
	ctx context.Context,
	path string,
	body []byte,
	sessionID string,
) browserAPIResponse {
	t.Helper()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		integrationInfra.APIURL(path),
		bytes.NewReader(body),
	)
	require.NoError(t, err)
	request.Header.Set(aichteeteapee.HeaderNameContentType, aichteeteapee.ContentTypeJSON)
	request.Header.Set(
		aichteeteapee.HeaderNameAuthorization,
		browserBearerPrefix+testinfra.TestAPIToken,
	)
	if sessionID != "" {
		request.Header.Set(browserSessionIDHeader, sessionID)
	}

	response, err := integrationInfra.HTTPClient().Do(request)
	require.NoError(t, err)

	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)

	return browserAPIResponse{status: response.StatusCode, body: data}
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

	requireNamedScreenshot(t, ctx, browserScreenshotBaseName)
}

func requireNamedScreenshot(t *testing.T, ctx context.Context, name string) {
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

		artifactDirectory, err = os.MkdirTemp(configuredRoot, name+"-")
		require.NoError(t, err)
	}

	artifactPath := filepath.Join(artifactDirectory, name+".png")
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
