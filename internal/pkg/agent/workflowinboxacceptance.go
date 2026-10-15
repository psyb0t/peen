package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (q *activeUserMessageQueue) enqueueWorkflowMessage(
	ctx context.Context,
	input TurnRequest,
) error {
	if input.RequestID == uuid.Nil {
		id, err := uuid.NewRandom()
		if err != nil {
			return ctxerrors.Wrap(err, "generate queued workflow request ID")
		}

		input.RequestID = id
	}

	receipt, err := q.acceptWorkflowMessage(ctx, input)
	if err != nil {
		return ctxerrors.Wrap(err, "accept native queued message")
	}

	return q.turn.projectStoredInbox(ctx, &session.ExecutionInboxDelivery{
		Events: receipt.Events,
	})
}

func (q *activeUserMessageQueue) acceptWorkflowMessage(
	ctx context.Context,
	input TurnRequest,
) (*session.ExecutionInboxAcceptance, error) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if q.closed {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"active workflow queue has completed")
	}

	store, ok := q.turn.store.(session.ExecutionStorage)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"active workflow inbox storage")
	}

	accepted, err := q.workflowAcceptanceInput(ctx, store, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve native queue acceptance")
	}

	receipt, err := store.AcceptExecutionMessage(ctx, q.executionID,
		q.turn.lease, accepted)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "persist native queue acceptance")
	}

	if len(receipt.Events) != 0 {
		q.workflowPending[input.RequestID] = 1 + len(accepted.Parts)
		for _, part := range accepted.Parts {
			if !part.SkillIsNote {
				q.skills.record(part.SkillName)
			}
		}

		close(q.arrived)
		q.arrived = make(chan struct{})
	}

	return receipt, nil
}

func (q *activeUserMessageQueue) workflowAcceptanceInput(
	ctx context.Context,
	store session.ExecutionStorage,
	input TurnRequest,
) (session.AcceptExecutionMessageInput, error) {
	accepted := session.AcceptExecutionMessageInput{
		RequestID: input.RequestID, SourceEventID: input.SourceEventID,
		WorkingDirectory: q.workspace, Message: input.Message,
		Capacity: q.capacity, ExpectedStopRevision: input.ExpectedStopRevision,
	}

	row, err := store.GetExecutionMessageByRequest(ctx, q.turn.lease.SessionID,
		q.executionID, input.RequestID)
	if err != nil && !errors.Is(err, commerr.ErrNotFound) {
		return accepted, ctxerrors.Wrap(err, "read previous queue acceptance")
	}

	if row != nil {
		accepted.WorkingDirectory = row.WorkingDirectory
		if err := json.Unmarshal([]byte(row.PartsJSON),
			&accepted.Parts); err != nil {
			return accepted, ctxerrors.Wrap(err,
				"restore accepted queue skills")
		}

		return accepted, nil
	}

	skills, err := buildSkillMessages(ctx, q.snapshot, q.skills, input.Message)
	if err != nil {
		return accepted, ctxerrors.Wrap(err, "resolve native queued skills")
	}

	for _, skill := range skills {
		accepted.Parts = append(accepted.Parts, session.ExecutionInboxPart{
			Content: skill.text, SkillName: skill.name,
			SkillIsNote: skill.isNote,
		})
	}

	return accepted, nil
}
