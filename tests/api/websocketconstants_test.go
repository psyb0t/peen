//go:build integration

package api_test

import (
	"time"
)

const (
	// apiTestWebSocketPath is its own literal, not derived from apiBasePath:
	// the WebSocket and the REST API are separate surfaces with separate
	// paths.
	apiTestWebSocketPath               = "/ws/v1"
	apiTestWebSocketSessionIDParameter = "sessionId"
	apiTestWebSocketSubprotocol        = "peen.v1"
	apiTestWebSocketBearerPrefix       = "peen.bearer." //nolint:gosec // A subprotocol prefix, not a credential.
	apiTestWebSocketMessageSend        = "message.send"
	apiTestWebSocketMessageCompleted   = "message.completed"
	apiTestWebSocketMessageFailed      = "message.failed"
	apiTestWebSocketTurnStarted        = "turn.started"
	apiTestWebSocketContentBlockDelta  = "content_block_delta"
	apiTestWebSocketTurnCompleted      = "turn.completed"
	apiTestWebSocketMessageDelivered   = "userMessage.delivered"
	apiTestWebSocketMetadataSessionID  = "sessionId"
	apiTestWebSocketMetadataRequestID  = "requestId"
	apiTestWebSocketMetadataSequence   = "sequence"
	apiTestWebSocketReadTimeout        = 30 * time.Second
	apiTestWebSocketIsolationTimeout   = time.Second
	apiTestWebSocketInitialMessage     = "open a synchronized websocket session"
	apiTestWebSocketActiveMessage      = "hold this websocket turn active"
	apiTestWebSocketQueuedMessage      = "queue this websocket message"
	apiTestWebSocketReasoningMessage   = "think about this websocket message"
	apiTestWebSocketReasoningEffort    = "high"

	apiTestWebSocketSkillMessage        = "plan this with :core:planning"
	apiTestWebSocketUnknownSkillMessage = "run :no-such-skill on this"

	apiTestWebSocketOrderedWorkspacePrefix = "admission-order-"
	apiTestWebSocketOrderedFixtureFile     = "README.md"
	apiTestWebSocketOrderedFixture         = "a workspace no test has opened yet"
	apiTestWebSocketFirstOrderedMessage    = "run the first ordered message"
	apiTestWebSocketSecondOrderedMessage   = "then answer the second ordered message"

	apiTestWebSocketUnattendedMessage             = "finish this turn with nobody attached"
	apiTestWebSocketUnattendedWorkspacePrefix     = "unattended-turn-"
	apiTestWebSocketFeedValidationWorkspacePrefix = "feed-validation-"
	apiTestWebSocketNoticeFeedWorkspacePrefix     = "notice-feed-"

	apiTestWebSocketReconnectMessage         = "catch up on this after a reconnect"
	apiTestWebSocketReconnectWorkspacePrefix = "reconnect-feed-"
	apiTestFrameNoticeCreated                = "notice.created"
	apiTestNoticeFeedType                    = "test.feed.notice"
	apiTestNoticeFeedSummary                 = "feed sequencing fixture notice"
	apiTestFeedReplayPageLimit               = 2
	apiTestFeedDefaultLimit                  = 100
)
