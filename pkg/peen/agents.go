package peen

import (
	"context"

	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// Agents addresses the children belonging to one session.
type Agents struct{ session *Session }

// AgentListRequest filters runs by state, including waiting, and direct parent.
type AgentListRequest struct {
	PageRequest
	State            AgentRunState
	ParentAgentRunID string
}

// Agents returns this session's child-agent control surface.
func (s *Session) Agents() Agents { return Agents{session: s} }

// List reads children and their pending questions.
func (a Agents) List(
	ctx context.Context, request AgentListRequest,
) (AgentRunPage, error) {
	limit, offset, err := pageParameters(request.PageRequest)
	if err != nil {
		return AgentRunPage{}, err
	}

	return sessionRead[AgentRunPage](ctx, a.session,
		func(ctx context.Context) (any, error) {
			params := api.ListSessionAgentRunsParams{
				XSessionID: a.session.id, Limit: limit, Offset: offset,
			}

			if request.State != "" {
				state := api.ListSessionAgentRunsParamsState(request.State)
				params.State = &state
			}

			if request.ParentAgentRunID != "" {
				params.ParentAgentRunId = &request.ParentAgentRunID
			}

			return a.session.core().ListSessionAgentRuns(ctx,
				a.session.id, params)
		})
}

// Get reads one child, refusing IDs belonging to another session.
func (a Agents) Get(ctx context.Context, runID string) (AgentRun, error) {
	id, err := parseRecordID(runID)
	if err != nil {
		return AgentRun{}, err
	}

	return sessionRead[AgentRun](ctx, a.session,
		func(ctx context.Context) (any, error) {
			return a.session.core().GetSessionAgentRun(ctx,
				a.session.id, id)
		})
}

// Cancel stops one child and its descendants without stopping the root.
func (a Agents) Cancel(
	ctx context.Context, runID string,
) (AgentRunCancelResponse, error) {
	id, err := parseRecordID(runID)
	if err != nil {
		return AgentRunCancelResponse{}, err
	}

	return sessionRead[AgentRunCancelResponse](ctx, a.session,
		func(ctx context.Context) (any, error) {
			return a.session.core().CancelSessionAgentRun(ctx,
				a.session.id, id)
		})
}

// Send queues a user message into a running child's inbox.
func (a Agents) Send(
	ctx context.Context, runID, message string,
) (MessageResult, error) {
	return a.session.Send(ctx, MessageRequest{Message: message, To: runID}, nil)
}

// Events follows the child's durable event sequence.
func (a Agents) Events(
	ctx context.Context,
	runID string,
	request CursorRequest,
) (AgentRunEventPage, error) {
	id, err := parseRecordID(runID)
	if err != nil {
		return AgentRunEventPage{}, err
	}

	limit, _, err := pageParameters(PageRequest{Limit: request.Limit})
	if err != nil {
		return AgentRunEventPage{}, err
	}

	return sessionRead[AgentRunEventPage](ctx, a.session,
		func(ctx context.Context) (any, error) {
			return a.session.core().ListSessionAgentRunEvents(ctx,
				a.session.id, id, api.ListSessionAgentRunEventsParams{
					XSessionID: a.session.id,
					Limit:      limit,
					Cursor:     &request.Cursor,
				})
		})
}

// Messages reads the child's own conversation in sequence order.
func (a Agents) Messages(
	ctx context.Context,
	runID string,
	request PageRequest,
) (AgentRunMessagePage, error) {
	id, err := parseRecordID(runID)
	if err != nil {
		return AgentRunMessagePage{}, err
	}

	limit, offset, err := pageParameters(request)
	if err != nil {
		return AgentRunMessagePage{}, err
	}

	return sessionRead[AgentRunMessagePage](ctx, a.session,
		func(ctx context.Context) (any, error) {
			return a.session.core().ListSessionAgentRunMessages(ctx,
				a.session.id, id, api.ListSessionAgentRunMessagesParams{
					XSessionID: a.session.id, Limit: limit, Offset: offset,
				})
		})
}

// Compactions reads the child's independent summary lineage.
func (a Agents) Compactions(
	ctx context.Context,
	runID string,
	request PageRequest,
) (AgentRunCompactionPage, error) {
	id, err := parseRecordID(runID)
	if err != nil {
		return AgentRunCompactionPage{}, err
	}

	limit, offset, err := pageParameters(request)
	if err != nil {
		return AgentRunCompactionPage{}, err
	}

	return sessionRead[AgentRunCompactionPage](ctx, a.session,
		func(ctx context.Context) (any, error) {
			return a.session.core().ListSessionAgentRunCompactions(
				ctx,
				a.session.id, id, api.ListSessionAgentRunCompactionsParams{
					XSessionID: a.session.id, Limit: limit, Offset: offset,
				})
		})
}

// Compaction reads a child's summary only when both IDs belong to the session.
func (a Agents) Compaction(
	ctx context.Context,
	runID, compactionID string,
) (AgentRunCompaction, error) {
	id, err := parseRecordID(runID)
	if err != nil {
		return AgentRunCompaction{}, err
	}

	summaryID, err := parseRecordID(compactionID)
	if err != nil {
		return AgentRunCompaction{}, err
	}

	read := func(ctx context.Context) (any, error) {
		return a.session.core().GetSessionAgentRunCompaction(
			ctx, a.session.id, id, summaryID,
		)
	}

	return sessionRead[AgentRunCompaction](ctx, a.session, read)
}
