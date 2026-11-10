package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
	"gorm.io/gorm"
)

// rootReportsClosed reports whether the root can no longer read its inbox
// for a child report: it has ended, or its completion is sealed and a report
// would be left behind. A running root keeps reading reports from its inbox.
func rootReportsClosed(
	ctx context.Context,
	tx *repositories.Query,
	root *models.Execution,
) (bool, error) {
	if isExecutionTerminal(root.State) {
		return true, nil
	}

	seal, err := readExecutionCompletionSeal(ctx, tx, root)
	if err != nil {
		return false, ctxerrors.Wrap(err, "read root completion seal")
	}

	return seal != nil, nil
}

// convertRootChildReports turns the child reports a finishing root never
// consumed into session notices in the finish transaction, so none is left
// behind in an inbox nothing reads again.
func (s *Store) convertRootChildReports(
	ctx context.Context,
	tx *repositories.Query,
	root *models.Execution,
) error {
	if root.Kind != models.ExecutionKindRoot {
		return nil
	}

	inbox := tx.ExecutionInboxMessage

	rows, err := inbox.WithContext(ctx).Where(
		inbox.SessionID.Eq(root.SessionID), inbox.ExecutionID.Eq(root.ID),
		inbox.State.Eq(string(models.InboxMessageStateQueued)),
	).Order(inbox.Sequence).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read unconsumed root inbox")
	}

	for _, row := range rows {
		if row.SourceEventID == nil {
			continue
		}

		converted, err := s.convertRootChildReport(ctx, tx, root,
			*row.SourceEventID)
		if err != nil {
			return ctxerrors.Wrap(err, "convert unconsumed child report")
		}

		if !converted {
			continue
		}

		if _, err := inbox.WithContext(ctx).Where(
			inbox.SessionID.Eq(root.SessionID), inbox.ID.Eq(row.ID),
		).Delete(); err != nil {
			return ctxerrors.Wrap(err, "retire converted child report")
		}
	}

	return nil
}

func (s *Store) convertRootChildReport(
	ctx context.Context,
	tx *repositories.Query,
	root *models.Execution,
	eventID uuid.UUID,
) (bool, error) {
	event, err := readRootChildTerminalEvent(ctx, tx, root, eventID)
	if err != nil || event == nil {
		return false, err
	}

	run, err := s.findAgentRunWithQuery(ctx, tx, root.SessionID,
		event.AgentRunID)
	if err != nil {
		return false, ctxerrors.Wrap(err, "read child report run")
	}

	if err := s.saveRootChildOutcome(ctx, tx, root, run, event); err != nil {
		return false, ctxerrors.Wrap(err, "save converted child report")
	}

	return true, nil
}

// readRootChildTerminalEvent returns the terminal event eventID names when it
// ends a direct child of root, or nil when it is anything else.
func readRootChildTerminalEvent(
	ctx context.Context,
	tx *repositories.Query,
	root *models.Execution,
	eventID uuid.UUID,
) (*models.AgentRunEvent, error) {
	repository := tx.AgentRunEvent

	event, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(root.SessionID), repository.ID.Eq(eventID),
	).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil //nolint:nilnil // No such event is not a failure.
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child report event")
	}

	if !IsAgentRunTerminalEventType(event.EventType) {
		return nil, nil //nolint:nilnil // Not a terminal report.
	}

	executions := tx.Execution

	child, err := executions.WithContext(ctx).Where(
		executions.SessionID.Eq(root.SessionID),
		executions.AgentRunID.Eq(event.AgentRunID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child report execution")
	}

	if child.ParentExecutionID == nil || *child.ParentExecutionID != root.ID {
		return nil, nil //nolint:nilnil // Not a direct child of the root.
	}

	return event, nil
}

// saveRootChildOutcome records a finished child of the root conversation as a
// session notice in the caller's transaction. The root has no durable inbox
// once its turn ends, so the notice is what wakes an idle root and what a
// running root reads at its next tool boundary.
func (s *Store) saveRootChildOutcome(
	ctx context.Context,
	tx *repositories.Query,
	parent *models.Execution,
	run *models.AgentRun,
	event *models.AgentRunEvent,
) error {
	var duration time.Duration
	if run.EndedAt != nil {
		duration = run.EndedAt.Sub(run.StartedAt).Truncate(time.Millisecond)
	}

	data, err := events.EncodeAgentResultData(events.AgentResultData{
		RunID: run.ID, AgentName: run.Name, State: string(run.State),
		DurationMs: duration.Milliseconds(), Duration: duration.String(),
		Response: run.ResponseText,
		FailureDetail: events.TruncateToRuneBoundary(run.FailureDetail,
			events.AgentFailureDetailBytes),
	}, events.DefaultLimits().MaxDataBytes)
	if err != nil {
		return ctxerrors.Wrap(err, "encode root child outcome")
	}

	noticeType, summary := rootChildOutcomeNotice(run)

	// A cancelled child was stopped on purpose, so its report waits for the
	// next turn instead of starting one.
	delivery := models.NoticeDeliveryWake
	if run.State == models.AgentRunStateCancelled ||
		parent.State == models.ExecutionStatePaused {
		delivery = models.NoticeDeliveryQueue
	}

	input, err := s.normalizeSessionNoticeInput(parent.SessionID,
		CreateSessionNoticeInput{
			ID: event.ID, Type: noticeType, Source: events.AgentSource,
			Summary: summary, DataJSON: string(data), Delivery: delivery,
			CreatedAt: event.CreatedAt,
		})
	if err != nil {
		return ctxerrors.Wrap(err, "validate root child outcome")
	}

	if _, err := s.createSessionNoticeWithQuery(ctx, tx, parent.SessionID,
		input); err != nil {
		return ctxerrors.Wrap(err, "queue root child outcome")
	}

	return nil
}

func rootChildOutcomeNotice(run *models.AgentRun) (events.Type, string) {
	if run.State == models.AgentRunStateCompleted {
		return events.TypeAgentFinished, fmt.Sprintf(
			events.AgentFinishedSummaryFormat, run.Name, run.ID)
	}

	return events.TypeAgentFailed, fmt.Sprintf(
		events.AgentFailedSummaryFormat, run.Name, run.ID, run.State)
}
