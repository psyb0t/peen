package server

import "github.com/psyb0t/aichteeteapee"

// Peen's documented contract distinguishes four session-scoped failures that
// aichteeteapee's generic codes would otherwise collapse into NOT_FOUND,
// CONFLICT, and BAD_REQUEST. aichteeteapee.ErrorCode is a plain string alias,
// so Peen defines its own values on it rather than rolling a separate error
// envelope.
const (
	// ErrorCodeInvalidSessionID reports a malformed X-Session-ID header.
	ErrorCodeInvalidSessionID aichteeteapee.ErrorCode = "INVALID_SESSION_ID"
	// ErrorCodeSessionNotFound reports a well-formed but unknown session ID.
	ErrorCodeSessionNotFound aichteeteapee.ErrorCode = "SESSION_NOT_FOUND"
	// ErrorCodeSessionBusy reports a session with an already active turn.
	ErrorCodeSessionBusy aichteeteapee.ErrorCode = "SESSION_BUSY"
	// ErrorCodeTurnCancelled reports a turn that ended because its session
	// was explicitly cancelled.
	ErrorCodeTurnCancelled aichteeteapee.ErrorCode = "TURN_CANCELLED"
	// ErrorCodeUserMessageQueueFull reports an active turn that cannot accept
	// another queued user message.
	ErrorCodeUserMessageQueueFull = aichteeteapee.ErrorCode(
		"USER_MESSAGE_QUEUE_FULL",
	)
	// ErrorCodeAgentSpaceNotAllowed reports a named agent space the
	// deployment does not offer. It is a refusal rather than a not-found,
	// because the allowed set is operator policy and the response never names
	// which agent spaces exist.
	ErrorCodeAgentSpaceNotAllowed = aichteeteapee.ErrorCode(
		"AGENT_SPACE_NOT_ALLOWED",
	)
	// ErrorCodeSystemAgentOff reports a system session request while
	// PEEN_SYSTEM_AGENT is off.
	ErrorCodeSystemAgentOff = aichteeteapee.ErrorCode("SYSTEM_AGENT_OFF")
	// ErrorCodeWorkspaceNotAllowed reports a requested workspace that falls
	// outside every configured workspace root. It is distinct from a
	// validation failure because the path is well formed and the deployment
	// simply does not expose it.
	ErrorCodeWorkspaceNotAllowed = aichteeteapee.ErrorCode(
		"WORKSPACE_NOT_ALLOWED",
	)
	// ErrorCodeWorkspaceNotFound reports an admitted workspace path whose
	// directory no longer exists.
	ErrorCodeWorkspaceNotFound = aichteeteapee.ErrorCode(
		"WORKSPACE_NOT_FOUND",
	)
	// ErrorCodeAgentFinished reports a message sent to a child agent that has
	// already ended.
	ErrorCodeAgentFinished = aichteeteapee.ErrorCode("AGENT_FINISHED")
	// ErrorCodeAgentNotFound reports a message sent to an agent run the
	// session never had.
	ErrorCodeAgentNotFound = aichteeteapee.ErrorCode("AGENT_NOT_FOUND")
	// ErrorCodeHarnessConfigurationInvalid reports a harness safety bound that
	// prevents a turn from starting. Malformed optional definitions emit a
	// harness.warning event instead.
	ErrorCodeHarnessConfigurationInvalid = aichteeteapee.ErrorCode(
		"HARNESS_CONFIGURATION_INVALID",
	)
	// ErrorCodeTurnRoundLimit reports a turn that stopped at its limit of
	// tool rounds, PEEN_MAX_TOOL_ROUNDS.
	ErrorCodeTurnRoundLimit = aichteeteapee.ErrorCode("TURN_ROUND_LIMIT")
	// ErrorCodeTurnTokenLimit reports a turn that stopped after billing its
	// token limit, PEEN_MAX_TURN_TOKENS.
	ErrorCodeTurnTokenLimit = aichteeteapee.ErrorCode("TURN_TOKEN_LIMIT")
)
