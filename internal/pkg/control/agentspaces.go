package control

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

// AgentSpaces reports the agent spaces a client may name, with the grants of
// each Docker space and the capability warnings for any risky grant.
func (r *Registry) AgentSpaces() api.AgentSpaceList {
	spaces := r.agentSpaces.All()
	items := make([]api.AgentSpace, 0, len(spaces))

	for _, space := range spaces {
		items = append(items, agentSpaceToAPI(space))
	}

	return api.AgentSpaceList{
		Items:   items,
		Default: r.agentSpaces.Default(),
	}
}

// WorkspaceRoots reports the directories this authenticated controller lets a
// client select when opening its first workspace session.
func (r *Registry) WorkspaceRoots() api.WorkspaceRootList {
	return api.WorkspaceRootList{Roots: r.Roots()}
}

func agentSpaceToAPI(space worker.AgentSpace) api.AgentSpace {
	converted := api.AgentSpace{
		Name:               space.Name,
		Kind:               api.AgentSpaceKind(space.Kind),
		Revision:           space.Revision,
		HostRootEquivalent: space.HostRootEquivalent(),
		CapabilityWarnings: space.CapabilityWarnings(),
	}

	if space.Kind != worker.KindDocker {
		return converted
	}

	profile := space.DockerProfile()
	docker := api.AgentSpaceDocker(space.Docker)
	network := api.AgentSpaceNetwork(space.Network)
	sudo := space.Sudo

	converted.Profile = &profile
	converted.Docker = &docker
	converted.Network = &network
	converted.Sudo = &sudo

	return converted
}

// ReconfigureSession moves an idle session to a different agent space.
//
// The agent space must be one the deployment offers, the session must have no
// turn in flight, and the change is recorded with the caller's reason. The
// session's current worker is stopped, so the next turn starts a new generation
// in the new agent space rather than continuing in the old environment.
func (r *Registry) ReconfigureSession(
	ctx context.Context,
	sessionID uuid.UUID,
	request api.ReconfigureSessionRequest,
) (api.SessionAgentSpaceDecision, error) {
	agentSpace, err := r.agentSpaces.Select(request.AgentSpace)
	if err != nil {
		return api.SessionAgentSpaceDecision{}, ctxerrors.Wrap(
			err,
			"select agent space",
		)
	}

	decision, err := r.store.ReconfigureAgentSpace(
		ctx,
		sessionID,
		agentSpace.Name,
		request.Reason,
	)
	if err != nil {
		return api.SessionAgentSpaceDecision{}, ctxerrors.Wrap(
			err,
			"reconfigure session agent space",
		)
	}

	// Stopping the worker is what makes the new agent space take effect.
	// Without it the session would keep running in the process it already had.
	if r.workers != nil {
		if err := r.workers.Stop(ctx, sessionID); err != nil {
			ctxscope.GetLogger(ctx).Warn(
				"stopping the worker after reconfiguration failed",
				"reason", "worker_stop_failed",
				"session_id", sessionID.String(),
				"err", err,
			)
		}
	}

	ctxscope.GetLogger(ctx).Info(
		"session agent space changed",
		"session_id", sessionID.String(),
		"from_agent_space", decision.FromAgentSpace,
		"to_agent_space", decision.ToAgentSpace,
	)

	return agentSpaceDecisionToAPI(decision), nil
}

// ListAgentSpaceDecisions returns one page of a session's agent space decision
// history, newest first.
//
//nolint:dupl // Each read model converts its own distinct page type.
func (r *Registry) ListAgentSpaceDecisions(
	ctx context.Context,
	sessionID uuid.UUID,
	params api.ListSessionAgentSpaceDecisionsParams,
) (api.SessionAgentSpaceDecisionPage, error) {
	page, err := r.store.ListSessionAgentSpaceDecisions(
		ctx,
		sessionID,
		session.ListAgentSpaceDecisionsOptions{
			Limit:  optionalPageBound(params.Limit),
			Offset: optionalPageBound(params.Offset),
		},
	)
	if err != nil {
		return api.SessionAgentSpaceDecisionPage{}, ctxerrors.Wrap(
			err,
			"list session agent space decisions",
		)
	}

	items := make([]api.SessionAgentSpaceDecision, 0, len(page.Items))
	for _, stored := range page.Items {
		items = append(items, agentSpaceDecisionToAPI(stored))
	}

	limit, err := pageValueToAPI(page.Limit, "agent space decision page limit")
	if err != nil {
		return api.SessionAgentSpaceDecisionPage{}, err
	}

	offset, err := pageValueToAPI(
		page.Offset,
		"agent space decision page offset",
	)
	if err != nil {
		return api.SessionAgentSpaceDecisionPage{}, err
	}

	return api.SessionAgentSpaceDecisionPage{
		Items:   items,
		Limit:   limit,
		Offset:  offset,
		HasMore: page.HasMore,
	}, nil
}

func agentSpaceDecisionToAPI(
	stored *models.SessionAgentSpaceDecision,
) api.SessionAgentSpaceDecision {
	decision := api.SessionAgentSpaceDecision{
		Id:           stored.ID,
		SessionId:    stored.SessionID,
		ToAgentSpace: stored.ToAgentSpace,
		Reason:       stored.Reason,
		DecidedAt:    stored.DecidedAt,
	}

	if stored.FromAgentSpace != "" {
		decision.FromAgentSpace = &stored.FromAgentSpace
	}

	return decision
}

// ListWorkerGenerations returns one page of a session's worker generations,
// newest first.
//
//nolint:dupl // Each read model converts its own distinct page type.
func (r *Registry) ListWorkerGenerations(
	ctx context.Context,
	sessionID uuid.UUID,
	params api.ListSessionWorkersParams,
) (api.WorkerGenerationPage, error) {
	page, err := r.store.ListWorkerGenerations(
		ctx,
		sessionID,
		session.ListWorkerGenerationsOptions{
			Limit:  optionalPageBound(params.Limit),
			Offset: optionalPageBound(params.Offset),
		},
	)
	if err != nil {
		return api.WorkerGenerationPage{}, ctxerrors.Wrap(
			err,
			"list worker generations",
		)
	}

	items := make([]api.WorkerGeneration, 0, len(page.Items))
	for _, stored := range page.Items {
		items = append(items, workerGenerationToAPI(stored))
	}

	limit, err := pageValueToAPI(page.Limit, "worker page limit")
	if err != nil {
		return api.WorkerGenerationPage{}, err
	}

	offset, err := pageValueToAPI(page.Offset, "worker page offset")
	if err != nil {
		return api.WorkerGenerationPage{}, err
	}

	return api.WorkerGenerationPage{
		Items:   items,
		Limit:   limit,
		Offset:  offset,
		HasMore: page.HasMore,
	}, nil
}

func workerGenerationToAPI(
	stored *models.WorkerGeneration,
) api.WorkerGeneration {
	generation := api.WorkerGeneration{
		Id:                 stored.ID,
		SessionId:          stored.SessionID,
		Kind:               api.WorkerGenerationKind(stored.Kind),
		AgentSpace:         stored.AgentSpace,
		AgentSpaceRevision: stored.AgentSpaceRevision,
		State:              api.WorkerGenerationState(stored.State),
		Workspace:          stored.Workspace,
		CreatedAt:          stored.CreatedAt,
		StartedAt:          stored.StartedAt,
		EndedAt:            stored.EndedAt,
	}

	if stored.ContainerID != "" {
		generation.ContainerId = &stored.ContainerID
	}

	if stored.ImageDigest != "" {
		generation.ImageDigest = &stored.ImageDigest
	}

	if stored.FailureDetail != "" {
		generation.FailureDetail = &stored.FailureDetail
	}

	return generation
}
