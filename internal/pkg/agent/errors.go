package agent

import "errors"

var (
	// ErrInvalidModelReference reports a malformed provider/model value.
	ErrInvalidModelReference = errors.New("invalid qualified model reference")
	// ErrModelUnavailable reports a model discovery did not find.
	ErrModelUnavailable = errors.New("configured model is unavailable")
	// ErrEventsUnavailable reports a runtime built without an event bus.
	ErrEventsUnavailable = errors.New("session events are not configured")
	// ErrContextBudgetTooLarge reports a configured request budget larger
	// than the selected model's published context window.
	ErrContextBudgetTooLarge = errors.New(
		"context budget exceeds the model's context window",
	)

	// ErrInvalidAgentRunOptions reports unusable agent run registry
	// construction options.
	ErrInvalidAgentRunOptions = errors.New("invalid agent run registry options")
	// ErrInvalidAgentRunLimits reports a limit set that cannot bound agent
	// run work.
	ErrInvalidAgentRunLimits = errors.New("invalid agent run limits")
	// ErrAgentSpecificationInvalid reports a launch_agent call naming zero
	// or both of agent and agentDefinition.
	ErrAgentSpecificationInvalid = errors.New(
		"agent specification is invalid",
	)
	// ErrAgentDefinitionTooLarge reports an ad-hoc agentDefinition whose
	// instructions exceed the configured byte bound.
	ErrAgentDefinitionTooLarge = errors.New(
		"ad-hoc agent definition is too large",
	)
	// ErrAgentDepthExceeded reports a launch_agent call that would exceed
	// the configured maximum child agent depth.
	ErrAgentDepthExceeded = errors.New("child agent depth limit exceeded")
	// ErrTooManyAgentRuns reports a session already running its configured
	// maximum number of concurrent agent runs.
	ErrTooManyAgentRuns = errors.New("too many concurrent agent runs")

	// ErrInvalidCompactionOptions reports compaction settings that cannot
	// bound a summarization call.
	ErrInvalidCompactionOptions = errors.New("invalid compaction options")
	// ErrCompactionPromptInvalid reports a COMPACTION.md the deployment
	// supplied but that cannot be used as summarizer instructions.
	ErrCompactionPromptInvalid = errors.New("invalid compaction prompt")
	// ErrSystemPromptInvalid reports a SYSTEM.md or APPEND_SYSTEM.md the
	// deployment supplied but that cannot be used as a system prompt.
	ErrSystemPromptInvalid = errors.New("invalid system prompt file")
	// ErrSystemPromptTooLarge reports request-supplied prompt text over the
	// configured byte bound.
	ErrSystemPromptTooLarge = errors.New("system prompt exceeds its limit")
	// ErrMessageTooLarge reports a caller message over the configured byte
	// bound.
	ErrMessageTooLarge = errors.New("message exceeds its limit")
	// ErrCompactionPlanMismatch reports an assembled transcript that no
	// longer matches the reconstruction plan, so no durable range can be
	// named for it.
	ErrCompactionPlanMismatch = errors.New(
		"transcript no longer matches the reconstruction plan",
	)
	// ErrCompactionUnavailable reports that no eligible completed prefix
	// frees enough context for the configured summary allowance.
	ErrCompactionUnavailable = errors.New(
		"no eligible history can be compacted",
	)
	// ErrCompactionEmptySummary reports a summarization call that produced
	// no usable summary.
	ErrCompactionEmptySummary = errors.New("compaction produced no summary")
	// ErrCompactionInsufficient reports a transcript still over budget after
	// its summary replaced the covered prefix.
	ErrCompactionInsufficient = errors.New(
		"transcript is still over budget after compaction",
	)
)
