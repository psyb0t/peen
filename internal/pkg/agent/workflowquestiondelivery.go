package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (d *workflowDispatch) deliverChildQuestion(
	ctx context.Context,
	input workflowExecutionInput,
	question *session.ExecutionChildQuestion,
) error {
	store, err := d.notificationStore()
	if err != nil {
		return ctxerrors.Wrap(err, "resolve question delivery storage")
	}

	child, err := store.GetExecution(ctx, input.SessionID, input.ExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read questioning child execution")
	}

	if child.ParentExecutionID == nil {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"questioning child has no destination")
	}

	parent, err := store.GetExecution(ctx, input.SessionID,
		*child.ParentExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read question destination")
	}

	switch parent.Kind {
	case models.ExecutionKindChild:
		return d.notifyParent(ctx, parent.SessionID, parent.ID)
	case models.ExecutionKindRoot:
		if parent.State == models.ExecutionStatePaused {
			return nil
		}

		return d.runtime.publishRetainedQuestion(ctx, child, question)
	}

	return ctxerrors.Wrap(commerr.ErrInvalidState,
		"question destination kind is unavailable")
}

func (r *Runtime) publishRetainedQuestion(
	ctx context.Context,
	child *models.Execution,
	question *session.ExecutionChildQuestion,
) error {
	return r.publishRetainedAgentNotice(ctx,
		func() (*models.SessionNotice, error) {
			return r.retainedQuestionNotice(ctx, child, question)
		})
}

// publishRetainedChildOutcome offers the stored finished or failed notice of
// a root child to the bus and the wake decision. Nothing is published when
// the notice was already delivered.
func (r *Runtime) publishRetainedChildOutcome(
	ctx context.Context,
	child *models.Execution,
) error {
	return r.publishRetainedAgentNotice(ctx,
		func() (*models.SessionNotice, error) {
			return r.retainedChildOutcomeNotice(ctx, child)
		})
}

// publishRetainedAgentNotice publishes the notice find returns, once, and
// runs the wake decision for it. A nil notice means there is nothing to offer.
func (r *Runtime) publishRetainedAgentNotice(
	ctx context.Context,
	find func() (*models.SessionNotice, error),
) error {
	release, err := r.noticeGate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit retained agent notice publication")
	}

	var wakeNotice *events.Notice

	defer func() {
		release()
		// Wake admission reads notices under this same gate.
		r.considerPublishedQuestion(ctx, wakeNotice)
	}()

	if r.eventBus == nil {
		return nil
	}

	notice, err := find()
	if err != nil {
		return ctxerrors.Wrap(err, "read retained agent notice")
	}

	if notice == nil {
		return nil
	}

	if notice.State == models.NoticeStateDelivered {
		delete(r.publishedQuestionNotices, notice.ID)

		return nil
	}

	if _, published := r.publishedQuestionNotices[notice.ID]; published {
		return nil
	}

	data := json.RawMessage(notice.DataJSON)
	if err := r.eventLimits().Validate(notice.Summary, data); err != nil {
		return ctxerrors.Wrap(err, "bound retained agent notice publication")
	}

	published, err := r.eventBus.PublishContext(ctx, events.Notice{
		ID: notice.ID, SessionID: notice.SessionID, Type: notice.Type,
		Source: notice.Source, Summary: notice.Summary, Data: data,
		Delivery: string(notice.Delivery), CreatedAt: notice.CreatedAt,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "publish retained agent notice")
	}

	if r.publishedQuestionNotices == nil {
		r.publishedQuestionNotices = make(map[uuid.UUID]struct{})
	}

	r.publishedQuestionNotices[notice.ID] = struct{}{}
	wakeNotice = &published

	return nil
}

func (r *Runtime) retainedQuestionNotice(
	ctx context.Context,
	child *models.Execution,
	question *session.ExecutionChildQuestion,
) (*models.SessionNotice, error) {
	if child == nil || child.AgentRunID == nil || question == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"retained question identity")
	}

	page, err := r.store.ListSessionNotices(ctx, child.SessionID,
		session.ListSessionNoticesOptions{ID: &question.EventID, Limit: 1})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find owned question notice")
	}

	if page == nil || len(page.Items) != 1 {
		return nil, ctxerrors.Wrap(commerr.ErrNotFound,
			"retained question notice is missing")
	}

	notice := page.Items[0]
	if err := validateRetainedQuestionNotice(notice, child,
		question); err != nil {
		return nil, ctxerrors.Wrap(err, "validate retained question notice")
	}

	return notice, nil
}

func validateRetainedQuestionNotice(
	notice *models.SessionNotice,
	child *models.Execution,
	question *session.ExecutionChildQuestion,
) error {
	if notice == nil {
		return ctxerrors.Wrap(commerr.ErrNotFound,
			"retained question notice is empty")
	}

	if notice.ID != question.EventID || notice.SessionID != child.SessionID ||
		notice.Type != events.TypeAgentQuestion ||
		notice.Source != events.AgentSource {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"retained question notice type differs")
	}

	var data events.AgentNoticeData
	if err := json.Unmarshal([]byte(notice.DataJSON), &data); err != nil {
		return ctxerrors.Wrap(err, "decode retained question notice")
	}

	if data.RunID != *child.AgentRunID || data.Message != question.Question {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"retained question notice payload differs")
	}

	return nil
}

func (r *Runtime) retainedChildOutcomeNotice(
	ctx context.Context,
	child *models.Execution,
) (*models.SessionNotice, error) {
	if child == nil || child.AgentRunID == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"retained child outcome identity")
	}

	options := session.ListSessionNoticesOptions{
		State: models.NoticeStatePending,
	}

	for {
		page, err := r.store.ListSessionNotices(ctx, child.SessionID, options)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "find owned child outcome notice")
		}

		for _, notice := range page.Items {
			if isChildOutcomeNotice(notice, *child.AgentRunID) {
				return notice, nil
			}
		}

		if !page.HasMore {
			//nolint:nilnil // Already delivered is not a failure.
			return nil, nil
		}

		options.Offset += len(page.Items)
	}
}

func isChildOutcomeNotice(
	notice *models.SessionNotice,
	agentRunID uuid.UUID,
) bool {
	if notice.Source != events.AgentSource ||
		(notice.Type != events.TypeAgentFinished &&
			notice.Type != events.TypeAgentFailed) {
		return false
	}

	var data events.AgentNoticeData
	if err := json.Unmarshal([]byte(notice.DataJSON), &data); err != nil {
		return false
	}

	return data.RunID == agentRunID
}
