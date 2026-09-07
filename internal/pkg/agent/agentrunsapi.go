package agent

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// ListSessionAgentRuns reports the child agents a session has launched, newest
// first, with the same bounded limit/offset paging as GET /v1/messages. A
// session that never launched one returns an empty list rather than an error.
func (r *Runtime) ListSessionAgentRuns(
	ctx context.Context,
	sessionID uuid.UUID,
	params api.ListSessionAgentRunsParams,
) (*api.AgentRunPage, error) {
	registry, err := r.existingSessionAgentRuns(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	page := api.AgentRunPage{Agents: []api.AgentRun{}, HasMore: false}
	if registry == nil {
		return &page, nil
	}

	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	wanted := ""
	if params.State != nil {
		wanted = string(*params.State)
	}

	snapshots := agentRunSnapshots(registry)
	filtered := make([]AgentRunSnapshot, 0, len(snapshots))

	for _, snapshot := range snapshots {
		if wanted != "" && snapshot.State != wanted {
			continue
		}

		filtered = append(filtered, snapshot)
	}

	page.HasMore = offset+limit < len(filtered)

	for _, snapshot := range pageWindow(filtered, offset, limit) {
		page.Agents = append(page.Agents, agentRunToAPI(snapshot))
	}

	return &page, nil
}

// ListSessionAgentRunEvents follows one child agent. A running agent is
// served from its bounded buffer, so the reported dropped count tells a
// caller its view has a hole rather than leaving it to assume it saw
// everything. A finished agent, or one no longer present in the in-memory
// registry (most commonly a service restart), is served from its durable
// JSONL transcript instead, sharing the same cursor semantics so a caller can
// follow a run across that boundary without a special case.
func (r *Runtime) ListSessionAgentRunEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	params api.ListSessionAgentRunEventsParams,
) (*api.AgentRunEventPage, error) {
	registry, err := r.existingSessionAgentRuns(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	var run *AgentRun
	if registry != nil {
		run, _ = registry.Get(agentRunID)
	}

	cursor := int32OrZero(params.Cursor)
	limit := int32OrZero(params.Limit)

	events, state, next, dropped, err := r.readAgentRunEvents(
		ctx, sessionID, agentRunID, run, cursor, limit,
	)
	if err != nil {
		return nil, err
	}

	page := api.AgentRunEventPage{
		AgentRunId: agentRunID,
		State:      api.AgentRunEventPageState(state),
		Events:     make([]api.AgentRunEvent, 0, len(events)),
		NextCursor: int32(next),    //nolint:gosec // Bounded counter.
		Dropped:    int32(dropped), //nolint:gosec // Bounded counter.
	}

	for _, event := range events {
		converted, convertErr := agentRunEventToAPI(event)
		if convertErr != nil {
			return nil, convertErr
		}

		page.Events = append(page.Events, converted)
	}

	return &page, nil
}

// readAgentRunEvents dispatches to the live ring buffer for a still-running
// run and to the durable JSONL mirror for a finished run or one absent from
// the in-memory registry. run is nil when the registry has no entry for
// agentRunID at all, in which case the agent's name is unknown and the
// mirror is located by run ID alone; its terminal state can then only be
// reported as AgentRunStateCompleted, since the file itself carries no
// completion marker.
func (r *Runtime) readAgentRunEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	run *AgentRun,
	cursor, limit int,
) ([]AgentRunEvent, AgentRunState, int, int, error) {
	if run == nil {
		path, ok := findAgentRunTranscriptPath(
			r.configDirectory, sessionID, agentRunID,
		)
		if !ok {
			return nil, "", 0, 0, ctxerrors.Wrap(
				commerr.ErrNotFound, "agent run",
			)
		}

		events, next, err := readAgentRunTranscript(ctx, path, cursor, limit)
		if err != nil {
			return nil, "", 0, 0, err
		}

		return events, AgentRunStateCompleted, next, 0, nil
	}

	snapshot := run.Snapshot()
	if snapshot.State == AgentRunStateRunning {
		events, next, dropped := run.ReadEvents(cursor, limit)

		return events, snapshot.State, next, dropped, nil
	}

	transcript := newAgentRunTranscript(
		ctx, r.configDirectory, sessionID, snapshot.Name, agentRunID,
	)
	if transcript.path == "" {
		return nil, "", 0, 0, ctxerrors.Wrap(commerr.ErrNotFound, "agent run")
	}

	events, next, err := readAgentRunTranscript(
		ctx, transcript.path, cursor, limit,
	)
	if err != nil {
		return nil, "", 0, 0, err
	}

	return events, snapshot.State, next, 0, nil
}

// CancelSessionAgentRun cancels one child agent without ending its parent
// turn. Cancelling an unknown, completed, or already cancelled run reports the
// current state rather than failing, matching POST /v1/session/cancel.
func (r *Runtime) CancelSessionAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) (*api.AgentRunCancelResponse, error) {
	registry, err := r.existingSessionAgentRuns(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	if registry == nil {
		return nil, ctxerrors.Wrap(commerr.ErrNotFound, "agent run")
	}

	snapshot, found := registry.Cancel(agentRunID)
	if !found {
		return nil, ctxerrors.Wrap(commerr.ErrNotFound, "agent run")
	}

	return &api.AgentRunCancelResponse{
		AgentRunId:      snapshot.ID,
		CancelRequested: snapshot.State == AgentRunStateRunning,
		State:           api.AgentRunCancelResponseState(snapshot.State),
	}, nil
}

// existingSessionAgentRuns returns the session's registry WITHOUT creating
// one, so a read can never allocate state, and confirms the session exists.
func (r *Runtime) existingSessionAgentRuns(
	ctx context.Context,
	sessionID uuid.UUID,
) (*AgentRunRegistry, error) {
	if _, err := r.store.Get(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "resolve agent run session")
	}

	r.agentRunsMutex.Lock()
	defer r.agentRunsMutex.Unlock()

	return r.agentRuns[sessionID], nil
}

// agentRunSnapshots returns every run newest first, so a caller sees what it
// just launched at the top.
func agentRunSnapshots(registry *AgentRunRegistry) []AgentRunSnapshot {
	runs := registry.List()
	snapshots := make([]AgentRunSnapshot, 0, len(runs))

	for _, run := range runs {
		snapshots = append(snapshots, run.Snapshot())
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].StartedAt.After(snapshots[j].StartedAt)
	})

	return snapshots
}

// agentRunToAPI converts one snapshot. Every int32 narrowing is of a bounded
// counter or a nesting depth, none of which can reach int32.
//
//nolint:gosec // See above: bounded counters and a depth.
func agentRunToAPI(snapshot AgentRunSnapshot) api.AgentRun {
	run := api.AgentRun{
		AgentRunId:         snapshot.ID,
		Name:               snapshot.Name,
		Definition:         api.AgentRunDefinition(snapshot.Definition),
		Depth:              int32(snapshot.Depth),
		State:              api.AgentRunState(snapshot.State),
		StartedAt:          snapshot.StartedAt,
		EventBufferedCount: int32(snapshot.EventBufferedCount),
		EventDroppedCount:  int32(snapshot.EventDroppedCount),
	}

	if snapshot.ParentToolCallID != "" {
		run.ParentToolCallId = &snapshot.ParentToolCallID
	}

	if !snapshot.EndedAt.IsZero() {
		ended := snapshot.EndedAt
		run.EndedAt = &ended
	}

	return run
}

func agentRunEventToAPI(event AgentRunEvent) (api.AgentRunEvent, error) {
	converted := api.AgentRunEvent{
		Sequence:  int32(event.Sequence), //nolint:gosec // Bounded counter.
		Type:      event.Type,
		CreatedAt: event.CreatedAt,
	}

	if len(event.Payload) == 0 {
		return converted, nil
	}

	payload := map[string]any{}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return api.AgentRunEvent{}, ctxerrors.Wrap(
			err,
			"decode agent run event payload",
		)
	}

	converted.Payload = &payload

	return converted, nil
}
