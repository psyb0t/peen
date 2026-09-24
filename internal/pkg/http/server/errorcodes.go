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
	// ErrorCodeExecutionProfileNotAllowed reports a named execution profile
	// the deployment does not define. It is a refusal rather than a
	// not-found, because the allowed set is operator policy and the response
	// never names which profiles exist.
	ErrorCodeExecutionProfileNotAllowed = aichteeteapee.ErrorCode(
		"EXECUTION_PROFILE_NOT_ALLOWED",
	)
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
	// ErrorCodeHarnessConfigurationInvalid reports a harness safety bound that
	// prevents a turn from starting. Malformed optional definitions emit a
	// harness.warning event instead.
	ErrorCodeHarnessConfigurationInvalid = aichteeteapee.ErrorCode(
		"HARNESS_CONFIGURATION_INVALID",
	)
)
