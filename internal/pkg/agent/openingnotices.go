package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) peekOpeningEvents(
	ctx context.Context,
	sessionID uuid.UUID,
) (events.Batch, error) {
	options := session.ListSessionNoticesOptions{
		State: models.NoticeStatePending,
	}

	var batch events.Batch

	for {
		page, err := r.store.ListSessionNotices(ctx, sessionID, options)
		if err != nil {
			return events.Batch{}, ctxerrors.Wrap(err,
				"read pending opening notices")
		}

		batch.Notices = append(batch.Notices,
			sessionNoticeBatch(page.Items).Notices...)
		if !page.HasMore {
			return batch, nil
		}

		options.Offset += len(page.Items)
	}
}

func (r *Runtime) admitRootOpening(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.StartTurnInput,
	opening *turnOpening,
) (rootTurnAdmission, error) {
	release, err := r.noticeGate.acquire(ctx, true)
	if err != nil {
		return rootTurnAdmission{}, ctxerrors.Wrap(err,
			"admit opening notice consumption")
	}
	defer release()

	input.NoticeIDs = noticeBatchIDs(opening.pendingBatch)

	admitted, err := r.acquireRootTurn(ctx, sessionID, input,
		opening.pendingBatch)
	if err != nil {
		return rootTurnAdmission{}, ctxerrors.Wrap(err,
			"commit root opening admission")
	}

	opening.pendingBatch = admitted.batch
	opening.publishPending = admitted.created

	opening.pendingEvents = renderModelEvents(admitted.batch)
	if opening.pendingEvents != "" {
		opening.assembly.prompt = opening.assembly.prompt.UserText(
			opening.pendingEvents)
	}

	r.acknowledgeNoticeBatch(sessionID, admitted.batch)

	return admitted, nil
}

// The caller holds noticeGate through storage consumption and acknowledgement.
func (r *Runtime) acknowledgeNoticeBatch(
	sessionID uuid.UUID,
	batch events.Batch,
) {
	ids := noticeBatchIDs(batch)
	for _, id := range ids {
		delete(r.publishedQuestionNotices, id)
	}

	if r.eventBus != nil {
		r.eventBus.Discard(sessionID, ids)
	}
}

func noticeBatchIDs(batch events.Batch) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(batch.Notices))
	for _, notice := range batch.Notices {
		ids = append(ids, notice.ID)
	}

	return ids
}

func turnWakeNoticeID(input TurnRequest) *uuid.UUID {
	if input.Origin == nil {
		return nil
	}

	return &input.Origin.EventID
}

func (o *turnOpening) admissionInput(
	input TurnRequest,
	requestID uuid.UUID,
	workspace string,
	events []session.EventInput,
) session.StartTurnInput {
	revision := input.ExpectedStopRevision
	if revision == nil {
		revision = &o.opened.Session.StopRevision
	}

	return session.StartTurnInput{
		RequestID: requestID, Workspace: workspace, Events: events,
		WakeNoticeID: turnWakeNoticeID(input), ExpectedStopRevision: revision,
		Messages: turnStartMessages(turnStartContent{
			prompt:           input.Message,
			isPromptInjected: input.Origin != nil,
			skills:           o.assembly.skills,
			pendingEvents:    o.pendingEvents,
		}, workspace),
	}
}
