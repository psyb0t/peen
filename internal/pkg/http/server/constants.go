package server

import "github.com/psyb0t/aichteeteapee"

// Header names, auth schemes, and content types come from aichteeteapee rather
// than being retyped here. Only X-Session-ID is Peen's own, so it is the only
// one defined locally.
const (
	apiBaseURL         = "/v1"
	apiMountPattern    = apiBaseURL + "/{path...}"
	spaMountPattern    = "/{path...}"
	webSocketPath      = apiBaseURL + "/ws"
	privateMetricsPath = "/metrics"

	webDistRoot       = "web/dist"
	spaIndexFile      = "index.html"
	spaAllowedMethods = "GET, HEAD"

	serviceName          = "peen"
	metricRouteUnmatched = "unmatched"
	webSocketHubName     = serviceName

	headerAuthorization     = aichteeteapee.HeaderNameAuthorization
	headerRequestID         = aichteeteapee.HeaderNameXRequestID
	headerAccept            = aichteeteapee.HeaderNameAccept
	headerContentType       = aichteeteapee.HeaderNameContentType
	headerWebSocketProtocol = "Sec-WebSocket-Protocol"

	// headerSessionID selects the durable session and has no shared-library
	// equivalent because it is specific to Peen's contract.
	headerSessionID = "X-Session-ID"

	// bearerScheme is the scheme TOKEN, without the trailing space that
	// aichteeteapee.AuthSchemeBearer carries for prefix trimming. This code
	// splits the header on its space and compares the token, which rejects
	// malformed values that a prefix trim would accept.
	bearerScheme = "Bearer"

	webSocketSessionIDParameter = "sessionId"
	webSocketClientIDParameter  = "clientID"
	webSocketSubprotocol        = "peen.v1"
	webSocketMetadataSessionID  = "sessionId"
	webSocketMetadataRequestID  = "requestId"
	//nolint:gosec // Public protocol label, not a credential.
	webSocketBearerSubprotocolPrefix   = "peen.bearer."
	webSocketMessageSendEventType      = "message.send"
	webSocketMessageCompletedEventType = "message.completed"
	webSocketMessageFailedEventType    = "message.failed"

	mediaTypeJSON = aichteeteapee.ContentTypeJSON
	mediaTypeAny  = "*/*"

	internalServerErrorMessage           = "internal server error"
	validationFailureMessage             = "invalid request"
	invalidJSONBodyMessage               = "invalid JSON request body"
	workspaceDirectoryNotFoundMessage    = "workspace directory does not exist"
	eventTypeAndSummaryMessage           = "type and summary are required"
	jobSignalRequiredMessage             = "signal is required"
	invalidJobStateMessage               = "unknown job state filter"
	invalidJobStreamMessage              = "unknown job output stream"
	invalidJobSignalMessage              = "unknown job signal"
	invalidSessionIDMessage              = "invalid session id"
	invalidBearerTokenMessage            = "invalid bearer token"
	webSocketMessageRejectedMessage      = "websocket message rejected"
	webSocketMessageFailedMessage        = "websocket message failed"
	webSocketHarnessConfigurationMessage = "Peen could not start this turn because the workspace agent configuration is invalid."            //nolint:lll // Client-safe text must remain one sentence.
	webSocketInvalidSkillReason          = "Each directory in .agents/skills must contain a SKILL.md file whose name matches the directory." //nolint:lll // Client-safe text must remain one sentence.
	webSocketInvalidInstructionReason    = "Check AGENTS.md and .agents/rules files for valid, nonempty instructions."                       //nolint:lll // Client-safe text must remain one sentence.
	webSocketInvalidAgentReason          = "Check .agents/agents for a valid named-agent document."                                          //nolint:lll // Client-safe text must remain one sentence.
	webSocketInvalidHookReason           = "Check .agents/hooks.yaml and .agents/events for valid handler definitions."                      //nolint:lll // Client-safe text must remain one sentence.
	webSocketHarnessLimitReason          = "Reduce the number or size of files in the workspace agent configuration."                        //nolint:lll // Client-safe text must remain one sentence.
	webSocketUnreadableHarnessReason     = "Check that the workspace agent configuration files are readable."                                //nolint:lll // Client-safe text must remain one sentence.
	unsupportedResponseMessage           = "unsupported response representation"
	httpRequestRejectedLogMessage        = "HTTP request rejected"
	httpRequestFailedLogMessage          = "HTTP request failed"

	// errorLocationMarker opens the "[file:line in func]" suffix ctxerrors
	// appends to every wrapped error. Everything from here on is diagnostic
	// detail for logs, never for a response body.
	errorLocationMarker = " ["

	maximumRequestBodyBytes int64 = 1 << 20
)
