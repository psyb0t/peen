package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
)

func validateChildQuestionProjection(input *ExecutionChildProjection) error {
	question := input.Question
	if question == nil {
		return nil
	}

	if question.EventID == uuid.Nil ||
		strings.TrimSpace(question.Question) == "" ||
		strings.TrimSpace(question.ParentMessage) == "" {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"child question identity and text")
	}

	for _, event := range input.Events {
		if event.ID != question.EventID {
			continue
		}

		if event.EventType != events.TypeAgentRunQuestion {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"child question event type")
		}

		var payload events.AgentQuestionPayload
		if err := json.Unmarshal([]byte(event.PayloadJSON),
			&payload); err != nil {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"child question event payload")
		}

		if payload.Question != question.Question {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"child question differs from its event")
		}

		return nil
	}

	return ctxerrors.Wrap(commerr.ErrValidationFailed,
		"child question event missing from projection")
}

func (s *Store) saveChildQuestion(
	ctx context.Context,
	query *repositories.Query,
	child *models.Execution,
	run *models.AgentRun,
	question *ExecutionChildQuestion,
) error {
	if question == nil {
		return nil
	}

	parent, err := s.childQuestionParent(ctx, query, child, run)
	if err != nil {
		return ctxerrors.Wrap(err, "read child question destination")
	}

	repository := query.AgentRunEvent

	event, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(child.SessionID),
		repository.AgentRunID.Eq(run.ID),
		repository.ID.Eq(question.EventID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read committed child question event")
	}

	if parent.Kind == models.ExecutionKindRoot {
		return s.saveRootChildQuestion(ctx, query, parent, run, event, question)
	}

	return s.recordExecutionChildReport(ctx, query, child, event,
		question.ParentMessage)
}

func (s *Store) childQuestionParent(
	ctx context.Context,
	query *repositories.Query,
	child *models.Execution,
	run *models.AgentRun,
) (*models.Execution, error) {
	if child.ParentExecutionID == nil {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child question has no parent execution")
	}

	parent, err := s.findExecution(ctx, query, child.SessionID,
		*child.ParentExecutionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child question parent")
	}

	if parent.RootTurnID != child.RootTurnID ||
		parent.RootExecutionID != child.RootExecutionID ||
		!sameOptionalExecutionID(parent.AgentRunID, run.ParentAgentRunID) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child question parent lineage changed")
	}

	if parent.Kind == models.ExecutionKindChild &&
		isExecutionTerminal(parent.State) {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"child question parent has ended")
	}

	return parent, nil
}

func (s *Store) saveRootChildQuestion(
	ctx context.Context,
	query *repositories.Query,
	parent *models.Execution,
	run *models.AgentRun,
	event *models.AgentRunEvent,
	question *ExecutionChildQuestion,
) error {
	data, err := json.Marshal(events.AgentNoticeData{
		RunID: run.ID, AgentName: run.Name, Message: question.Question,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode root child question")
	}

	delivery := models.NoticeDeliveryWake
	if parent.State == models.ExecutionStatePaused {
		delivery = models.NoticeDeliveryQueue
	}

	input, err := s.normalizeSessionNoticeInput(parent.SessionID,
		CreateSessionNoticeInput{
			ID: event.ID, Type: events.TypeAgentQuestion,
			Source: events.AgentSource,
			Summary: fmt.Sprintf(events.AgentQuestionSummaryFormat,
				run.Name, run.ID),
			DataJSON: string(data), Delivery: delivery,
			CreatedAt: event.CreatedAt,
		})
	if err != nil {
		return ctxerrors.Wrap(err, "validate root child question")
	}

	if _, err := s.createSessionNoticeWithQuery(ctx, query, parent.SessionID,
		input); err != nil {
		return ctxerrors.Wrap(err, "queue root child question")
	}

	return nil
}
