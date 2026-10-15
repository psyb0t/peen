package session

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) validateExecutionFinishChild(
	input *FinishExecutionInput,
) error {
	if err := validateExecutionFinishTurn(input); err != nil {
		return err
	}

	if input.Child == nil {
		return validateChildReportWithoutOutcome(input.ChildReport)
	}

	child := input.Child
	if input.RootHolder != nil || input.Turn != nil ||
		!matchingChildFinishState(input.State, child.State) ||
		child.FailureDetail != input.FailureDetail {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"child terminal result differs from its execution")
	}

	values := []string{
		input.ChildReport,
		child.ResponseText, child.ResponseThinking, child.ResponseMessagesJSON,
		child.FinishReason, child.FailureClassification, child.FailureDetail,
	}
	for _, value := range values {
		if len(value) > s.maxStoredMessageBytes {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"child terminal result exceeds storage limit")
		}
	}

	_, err := normalizeAgentRunFinalization(FinalizeAgentRunInput(*child))
	if err != nil {
		return ctxerrors.Wrap(err, "validate child terminal record")
	}

	return nil
}

func matchingChildFinishState(
	execution models.ExecutionState,
	child models.AgentRunState,
) bool {
	return (execution == models.ExecutionStateCompleted &&
		child == models.AgentRunStateCompleted) ||
		(execution == models.ExecutionStateFailed &&
			child == models.AgentRunStateFailed)
}

func validateChildFinishClaim(
	execution *models.Execution,
	input *FinishExecutionInput,
) error {
	if input == nil || input.Child == nil {
		return nil
	}

	if execution.Kind != models.ExecutionKindChild ||
		execution.AgentRunID == nil {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child terminal result belongs to another execution kind")
	}

	return nil
}

func (s *Store) finishExecutionChild(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	input *FinishExecutionInput,
) error {
	if input.Child == nil {
		return nil
	}

	child, err := normalizeAgentRunFinalization(
		FinalizeAgentRunInput(*input.Child))
	if err != nil {
		return ctxerrors.Wrap(err, "normalize child terminal result")
	}

	run, err := s.finalizeAgentRunInTransaction(ctx, tx, execution.SessionID,
		*execution.AgentRunID, child)
	if err != nil {
		return ctxerrors.Wrap(err, "save execution child terminal record")
	}

	event, err := s.recordExecutionChildTerminal(ctx, tx, run)
	if err != nil {
		return ctxerrors.Wrap(err, "record child terminal event")
	}

	return s.recordExecutionChildReport(ctx, tx, execution, event,
		input.ChildReport)
}
