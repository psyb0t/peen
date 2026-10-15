package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) prepareRootWorkflowInbox(
	ctx context.Context,
	input workflowExecutionInput,
	conversation *workflowConversation,
	ledger *skillLedger,
) error {
	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"workflow inbox storage")
	}

	conversation.InboxIDs = nil

	options := session.ListExecutionInboxOptions{
		State: models.InboxMessageStateQueued,
	}
	for {
		page, err := store.ListExecutionMessages(ctx, input.SessionID,
			input.ExecutionID, options)
		if err != nil {
			return ctxerrors.Wrap(err, "read queued workflow inbox")
		}

		for _, row := range page.Items {
			if err := appendRootInboxContext(conversation, ledger,
				row); err != nil {
				return ctxerrors.Wrap(err, "append accepted inbox context")
			}
		}

		if !page.HasMore {
			return nil
		}

		options.Offset += len(page.Items)
	}
}

func appendRootInboxContext(
	conversation *workflowConversation,
	ledger *skillLedger,
	row *models.ExecutionInboxMessage,
) error {
	var parts []session.ExecutionInboxPart
	if err := json.Unmarshal([]byte(row.PartsJSON), &parts); err != nil {
		return ctxerrors.Wrap(err, "decode retained inbox skills")
	}

	conversation.InboxIDs = append(conversation.InboxIDs, row.RequestID)

	conversation.Messages = append(conversation.Messages, elelem.Message{
		Role: elelem.RoleUser, Content: elelem.Text(row.Message),
		Origin: elelem.MessageOriginTurn,
	})
	for _, part := range parts {
		conversation.Messages = append(conversation.Messages, elelem.Message{
			Role: elelem.RoleUser, Content: elelem.Text(part.Content),
			Origin: elelem.MessageOriginTurn,
		})
		if !part.SkillIsNote {
			ledger.record(part.SkillName)
		}
	}

	return nil
}

func (r *Runtime) deliverRootWorkflowInbox(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowExecutionInput,
	requestIDs []uuid.UUID,
) error {
	if len(requestIDs) == 0 {
		return nil
	}

	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"workflow inbox delivery storage")
	}

	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"workflow inbox delivery activity lease")
	}

	receipt, err := store.DeliverExecutionMessages(ctx, input.SessionID,
		session.DeliverExecutionMessagesInput{
			TurnLease: prepared.lease, ModelLease: lease,
			RequestIDs: requestIDs,
		})
	if err != nil {
		return ctxerrors.Wrap(err, "checkpoint root inbox delivery")
	}

	if prepared.userMessages != nil {
		prepared.userMessages.markWorkflowInboxDelivered(requestIDs)
	}

	return prepared.turn.projectStoredInbox(ctx, receipt)
}

func (p *preparedTurn) withWorkflowInboxContinuation(
	result workflowRoundResult,
) workflowRoundResult {
	if p.userMessages == nil {
		return result
	}

	pending, _ := p.userMessages.arrival()
	result.Continue = pending != 0

	return result
}
