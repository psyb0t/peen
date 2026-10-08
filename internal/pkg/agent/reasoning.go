package agent

import (
	"context"
	"slices"

	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
)

const (
	reasoningEffortReasonUnsupported  = "model_does_not_take_reasoning_effort"
	reasoningEffortReasonAboveMaximum = "above_model_maximum"
	reasoningEffortReasonBelowMinimum = "below_model_minimum"
)

// requestableReasoningEfforts returns the reasoning levels a message may ask
// for, lowest first. The order is what clamping to a model's range compares.
func requestableReasoningEfforts() []elelem.ReasoningEffort {
	return []elelem.ReasoningEffort{
		elelem.ReasoningEffortMinimal,
		elelem.ReasoningEffortLow,
		elelem.ReasoningEffortMedium,
		elelem.ReasoningEffortHigh,
		elelem.ReasoningEffortXHigh,
		elelem.ReasoningEffortMax,
	}
}

func isRequestableReasoningEffort(effort elelem.ReasoningEffort) bool {
	return slices.Contains(requestableReasoningEfforts(), effort)
}

func reasoningEffortRank(effort elelem.ReasoningEffort) int {
	return slices.Index(requestableReasoningEfforts(), effort)
}

// turnReasoningEffort fits the turn's requested level to its model and logs
// any change, so an operator can see why a call ran at another level.
func turnReasoningEffort(
	ctx context.Context,
	prepared *preparedTurn,
) elelem.ReasoningEffort {
	model := prepared.model.Model

	effort, reason := fitReasoningEffort(
		prepared.reasoningEffort,
		model,
		prepared.model.Client.Capabilities(model),
	)
	if reason != "" {
		ctxscope.GetLogger(ctx).Info(
			"reasoning effort fitted to the model",
			"model", prepared.modelReference,
			"requested_reasoning_effort", prepared.reasoningEffort,
			"reasoning_effort", effort,
			"reason", reason,
		)
	}

	return effort
}

// fitReasoningEffort returns the level a model call can actually carry, and
// the reason it differs from the requested one.
//
// A client may send a level with every turn whatever model the turn uses, and
// Elelem rejects a request whose level the model does not take. So a model
// without effort support gets none, and a level outside the model's range is
// clamped to its nearest end. The reason is empty when the level is used as
// asked.
func fitReasoningEffort(
	requested elelem.ReasoningEffort,
	model elelem.Model,
	capabilities elelem.Capabilities,
) (elelem.ReasoningEffort, string) {
	if requested == elelem.ReasoningEffortUnset {
		return elelem.ReasoningEffortUnset, ""
	}

	if !capabilities.SupportsReasoningEffort {
		return elelem.ReasoningEffortUnset, reasoningEffortReasonUnsupported
	}

	rank := reasoningEffortRank(requested)

	maximum := capabilities.MaxReasoningEffort

	maximumRank := reasoningEffortRank(maximum)
	if maximumRank >= 0 && rank > maximumRank {
		return maximum, reasoningEffortReasonAboveMaximum
	}

	minimum := model.ReasoningLevelMin()

	minimumRank := reasoningEffortRank(minimum)
	if minimumRank >= 0 && rank < minimumRank {
		return minimum, reasoningEffortReasonBelowMinimum
	}

	return requested, ""
}
