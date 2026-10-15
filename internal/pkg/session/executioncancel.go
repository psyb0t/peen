package session

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"math"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) cancelNativeChildTree(
	ctx context.Context,
	tx *repositories.Query,
	run *models.AgentRun,
) error {
	repository := tx.Execution

	target, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(run.SessionID),
		repository.AgentRunID.Eq(run.ID),
	).First()
	if err != nil {
		err = ctxerrors.Wrap(err, "find cancelled child execution")
		if errors.Is(err, commerr.ErrNotFound) {
			return nil
		}

		return err
	}

	members, err := repository.WithContext(ctx).Subtree(
		run.SessionID, target.ID,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "read cancelled child subtree")
	}

	for _, member := range members {
		execution, err := s.findExecution(ctx, tx, run.SessionID, member.ID)
		if err != nil {
			return ctxerrors.Wrap(err, "read cancelled execution context")
		}

		if isExecutionTerminal(execution.State) {
			continue
		}

		if err := s.cancelNativeChild(ctx, tx, execution); err != nil {
			return ctxerrors.Wrap(err, "settle cancelled child")
		}
	}

	stored, err := s.findAgentRunWithQuery(ctx, tx, run.SessionID, run.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "read cancelled child outcome")
	}

	*run = *stored

	return nil
}

func (s *Store) cancelNativeChild(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
) error {
	if execution.AgentRunID == nil ||
		execution.ControlRevision == math.MaxInt64 {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"cancelled child identity or revision is invalid")
	}

	now := s.now()
	repository := tx.Execution

	_, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ID.Eq(execution.ID),
	).UpdateSimple(
		repository.State.Value(string(models.ExecutionStateCancelled)),
		repository.ControlRevision.Add(1),
		repository.CompletedAt.Value(now), repository.UpdatedAt.Value(now),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "cancel execution record")
	}

	if err := s.cancelExecutionActivities(ctx, tx, execution); err != nil {
		return ctxerrors.Wrap(err, "cancel unfinished child activities")
	}

	payload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: ExecutionUserCancelReason})
	if err != nil {
		return ctxerrors.Wrap(err, "encode child cancellation")
	}

	if err := s.appendExecutionEvent(ctx, tx, execution,
		ExecutionEventTypeCancelled, payload); err != nil {
		return ctxerrors.Wrap(err, "retain child cancellation decision")
	}

	return s.cancelExecutionAgentRun(ctx, tx, execution)
}

func (s *Store) cancelExecutionActivities(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
) error {
	repository := tx.ExecutionActivity

	activities, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ExecutionID.Eq(execution.ID),
		repository.State.NotIn(string(models.ActivityStateCompleted),
			string(models.ActivityStateCancelled)),
	).Order(repository.CreatedAt, repository.ID).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read unfinished cancelled activities")
	}

	for _, activity := range activities {
		_, err := repository.WithContext(ctx).Where(
			repository.SessionID.Eq(execution.SessionID),
			repository.ID.Eq(activity.ID),
		).UpdateSimple(
			repository.State.Value(string(models.ActivityStateCancelled)),
			repository.CompletedAt.Value(s.now()),
		)
		if err != nil {
			return ctxerrors.Wrap(err, "settle cancelled activity")
		}

		if err := s.appendActivityEvent(ctx, tx, execution, activity,
			ExecutionEventTypeStepCancelled); err != nil {
			return ctxerrors.Wrap(err, "retain activity cancellation")
		}
	}

	return nil
}

func (s *Store) cancelExecutionAgentRun(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
) error {
	run, err := s.findAgentRunWithQuery(ctx, tx, execution.SessionID,
		*execution.AgentRunID)
	if err != nil {
		return ctxerrors.Wrap(err, "read cancelled agent run")
	}

	if run.State != models.AgentRunStateRunning &&
		run.State != models.AgentRunStateInterrupted {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"unfinished child has a terminal agent run")
	}

	now := s.now()
	repository := tx.AgentRun

	_, err = repository.WithContext(ctx).Where(
		repository.SessionID.Eq(run.SessionID), repository.ID.Eq(run.ID),
	).UpdateSimple(
		repository.State.Value(string(models.AgentRunStateCancelled)),
		repository.CancelRequested.Value(true), repository.EndedAt.Value(now),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "settle cancelled agent run")
	}

	run.State = models.AgentRunStateCancelled
	run.CancelRequested, run.EndedAt = true, &now

	event, err := s.recordExecutionChildTerminal(ctx, tx, run)
	if err != nil {
		return ctxerrors.Wrap(err, "retain cancelled child event")
	}

	report, err := cancelledChildReport(run)
	if err != nil {
		return ctxerrors.Wrap(err, "frame child cancellation report")
	}

	return s.recordExecutionChildReport(ctx, tx, execution, event, report)
}

func cancelledChildReport(run *models.AgentRun) (string, error) {
	encoded, err := xml.Marshal(struct {
		XMLName xml.Name `xml:"message"`
		From    string   `xml:"from,attr"`
		Name    string   `xml:"name,attr"`
		Run     string   `xml:"run,attr"`
		Kind    string   `xml:"kind,attr"`
		Text    string   `xml:",chardata"`
	}{
		From: AgentMessageSenderAgent, Name: run.Name, Run: run.ID.String(),
		Kind: string(models.AgentRunStateCancelled),
		Text: ExecutionUserCancelReason,
	})
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode child cancellation report")
	}

	return string(encoded), nil
}
