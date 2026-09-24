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

// ExecutionProfiles reports the profiles a client may name, with the
// capability warning for any that grants host-root-equivalent access.
func (r *Registry) ExecutionProfiles() api.ExecutionProfileList {
	profiles := r.profiles.All()
	items := make([]api.ExecutionProfile, 0, len(profiles))

	for _, profile := range profiles {
		items = append(items, executionProfileToAPI(profile))
	}

	return api.ExecutionProfileList{
		Items:   items,
		Default: r.profiles.Default(),
	}
}

// WorkspaceRoots reports the directories this authenticated controller lets a
// client select when opening its first workspace session.
func (r *Registry) WorkspaceRoots() api.WorkspaceRootList {
	return api.WorkspaceRootList{Roots: r.Roots()}
}

func executionProfileToAPI(profile worker.Profile) api.ExecutionProfile {
	converted := api.ExecutionProfile{
		Name:                     profile.Name,
		Kind:                     api.ExecutionProfileKind(profile.Kind),
		Revision:                 profile.Revision,
		AllowDockerSocket:        profile.AllowDockerSocket,
		AllowNetwork:             profile.AllowNetwork,
		AllowPrivilegeEscalation: profile.AllowPrivilegeEscalation,
		HostRootEquivalent:       profile.HostRootEquivalent(),
	}

	if warning := profile.CapabilityWarning(); warning != "" {
		converted.CapabilityWarning = &warning
	}

	return converted
}

// ReconfigureSession moves an idle session to a different execution profile.
//
// The profile must be one the operator defined, the session must have no turn
// in flight, and the change is recorded with the caller's reason. The session's
// current worker is stopped, so the next turn starts a new generation under the
// new profile rather than continuing in the old environment.
func (r *Registry) ReconfigureSession(
	ctx context.Context,
	sessionID uuid.UUID,
	request api.ReconfigureSessionRequest,
) (api.SessionProfileDecision, error) {
	profile, err := r.profiles.Select(request.Profile)
	if err != nil {
		return api.SessionProfileDecision{}, ctxerrors.Wrap(
			err,
			"select execution profile",
		)
	}

	decision, err := r.store.ReconfigureExecutionProfile(
		ctx,
		sessionID,
		profile.Name,
		request.Reason,
	)
	if err != nil {
		return api.SessionProfileDecision{}, ctxerrors.Wrap(
			err,
			"reconfigure session execution profile",
		)
	}

	// Stopping the worker is what makes the new profile take effect. Without
	// it the session would keep running in the process it already had.
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
		"session execution profile changed",
		"session_id", sessionID.String(),
		"from_profile", decision.FromProfile,
		"to_profile", decision.ToProfile,
	)

	return profileDecisionToAPI(decision), nil
}

// ListProfileDecisions returns one page of a session's profile decision
// history, newest first.
//
//nolint:dupl // Each read model converts its own distinct page type.
func (r *Registry) ListProfileDecisions(
	ctx context.Context,
	sessionID uuid.UUID,
	params api.ListSessionProfileDecisionsParams,
) (api.SessionProfileDecisionPage, error) {
	page, err := r.store.ListSessionProfileDecisions(
		ctx,
		sessionID,
		session.ListProfileDecisionsOptions{
			Limit:  optionalPageBound(params.Limit),
			Offset: optionalPageBound(params.Offset),
		},
	)
	if err != nil {
		return api.SessionProfileDecisionPage{}, ctxerrors.Wrap(
			err,
			"list session profile decisions",
		)
	}

	items := make([]api.SessionProfileDecision, 0, len(page.Items))
	for _, stored := range page.Items {
		items = append(items, profileDecisionToAPI(stored))
	}

	limit, err := pageValueToAPI(page.Limit, "profile decision page limit")
	if err != nil {
		return api.SessionProfileDecisionPage{}, err
	}

	offset, err := pageValueToAPI(page.Offset, "profile decision page offset")
	if err != nil {
		return api.SessionProfileDecisionPage{}, err
	}

	return api.SessionProfileDecisionPage{
		Items:   items,
		Limit:   limit,
		Offset:  offset,
		HasMore: page.HasMore,
	}, nil
}

func profileDecisionToAPI(
	stored *models.SessionProfileDecision,
) api.SessionProfileDecision {
	decision := api.SessionProfileDecision{
		Id:        stored.ID,
		SessionId: stored.SessionID,
		ToProfile: stored.ToProfile,
		Reason:    stored.Reason,
		DecidedAt: stored.DecidedAt,
	}

	if stored.FromProfile != "" {
		decision.FromProfile = &stored.FromProfile
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
		Id:              stored.ID,
		SessionId:       stored.SessionID,
		Kind:            api.WorkerGenerationKind(stored.Kind),
		Profile:         stored.Profile,
		ProfileRevision: stored.ProfileRevision,
		State:           api.WorkerGenerationState(stored.State),
		Workspace:       stored.Workspace,
		CreatedAt:       stored.CreatedAt,
		StartedAt:       stored.StartedAt,
		EndedAt:         stored.EndedAt,
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
