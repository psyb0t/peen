//go:build browser

package browser_test

import (
	"time"

	"github.com/psyb0t/peen/tests/testinfra"
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
	browserWebSocketPath                 = "/ws/v1"
	browserModelsPath                    = "/api/v1/models"
	browserWorkspaceRootsPath            = "/api/v1/workspace-roots"
	browserSessionsOpenPath              = "/api/v1/sessions/open"
	browserMessagesPath                  = "/api/v1/messages"
	browserTestWorkspace                 = "/tmp"
	browserChildWorkspaceName            = "browser-child-workspace"
	browserChildWorkspace                = browserTestWorkspace + "/" + browserChildWorkspaceName
	browserBrokenSkillFixture            = browserChildWorkspaceName + "/.agents/skills/broken-skill/ignored.txt"
	browserBrokenSkillContent            = "this is not a skill document\n"
	browserHarnessWarningLabel           = "Workspace configuration warning"
	browserBrokenSkillWarningSource      = "skill at /tmp/browser-child-workspace/.agents/skills/broken-skill:"
	browserBrokenSkillWarningReason      = "has no SKILL.md"
	browserThinkingMessage               = "browser control surface thinking fixture"
	browserTestMessage                   = "browser control surface fixture message"
	browserCompletionText                = "browser tool turn completed"
	browserFixtureName                   = "browser-control-surface.md"
	browserFixturePath                   = testinfra.ContainerWorkingDirectory + "/" + browserFixtureName
	browserFixtureContent                = "before\n"
	browserFixturePatch                  = "*** Begin Patch\n*** Update File: /tmp/browser-control-surface.md\n@@\n-before\n+after\n*** End Patch"
	browserFixtureCommand                = "test -f /tmp/browser-control-surface.md"
	browserFixtureCommandPurpose         = "confirm the browser fixture remains available"
	browserMainHeading                   = "Code with a durable agent."
	browserThinkingLabel                 = "Thinking"
	browserToolReadFile                  = "readFile"
	browserToolApplyPatch                = "applyPatch"
	browserToolRunCommand                = "runCommand"
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
	browserSessionNoticesPath  = "/api/v1/session/notices"
	browserBearerPrefix        = "Bearer "
	browserScreenshotBaseName  = "control-surface"
	browserSkillScreenshot     = "skill-marker"
	browserLiveSkillScreenshot = "live-skill-marker"
	browserHandlerScreenshot   = "handler-update"

	browserAgentsWorkspaceName = "browser-child-agents"
	browserAgentsMessage       = "launch a reviewer that asks first"
	browserAgentsTask          = "ask which file to review"
	browserAgentsQuestion      = "Which file should I review?"
	browserAgentsRootAnswer    = "the reviewer is running"
	browserAgentsCoreAgent     = "core:general-purpose"
	browserAgentsPanelLabel    = "running agents"
	browserAgentsAskPrefix     = "Asks: "
	browserSelectorStopAgent   = `section.running-agents button[aria-label^="Stop "]`
	browserAgentsCancelPrefix  = "/api/v1/session/agents/"
	browserAgentsCancelSuffix  = "/cancel"
	browserAgentsScreenshot    = "running-agents"
	browserAgentsPollEvery     = 250 * time.Millisecond

	browserLinkWorkspaceName  = "browser-session-link"
	browserSessionLinkFormat  = "/?session=%s"
	browserSelectorChatHeader = "header.chat-header h1"
	browserLinkScreenshot     = "session-link"

	// browserEvalLiveTurnGone reports whether the stored transcript has
	// replaced every live turn. The replacement renders new tool cards, so
	// a card expanded before it collapses again.
	browserEvalLiveTurnGone = `(() => {
		const live = document.querySelector("article.message.assistant.live");
		return String(live === null);
	})()`
	browserEvalTrue            = "true"
	browserTranscriptPollEvery = 250 * time.Millisecond

	browserActionEval             = "eval"
	browserExpressionKey          = "expression"
	browserReconnectWorkspaceName = "browser-reconnect-midturn"
	browserReconnectMessage       = "hold this websocket turn across a drop"
	browserReconnectScreenshot    = "reconnect-midturn"

	// browserEvalInstallSocketCapture wraps the global WebSocket constructor
	// so every socket the page opens is reachable afterward, since the
	// control surface keeps its own socket private. It installs once: a
	// second install is a no-op that reports the socket list already in
	// place.
	browserEvalInstallSocketCapture = `(() => {
		if (window.__peenOriginalWebSocket) {
			return "already-installed";
		}
		window.__peenOriginalWebSocket = window.WebSocket;
		window.__peenSockets = [];
		window.WebSocket = class extends window.__peenOriginalWebSocket {
			constructor(...args) {
				super(...args);
				window.__peenSockets.push(this);
			}
		};
		return "installed";
	})()`

	// browserEvalDropLastSocket closes the most recently opened captured
	// socket from the page's own side, the way a dropped connection looks to
	// the control surface: an unexpected close it did not request itself.
	browserEvalDropLastSocket = `(() => {
		const sockets = window.__peenSockets || [];
		const socket = sockets[sockets.length - 1];
		if (!socket) {
			return false;
		}
		socket.close();
		return true;
	})()`
)

func expectedBrowserDiagnostics() []string {
	return []string{
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
}
