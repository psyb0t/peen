package control

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/supervisor"
)

// RegistryOptions supplies the durable store and the policy a registry opens
// workspaces under.
type RegistryOptions struct {
	Store           *session.Store
	Policy          *WorkspacePolicy
	RootAgent       string
	DefaultModel    string
	MaxFileAPIBytes int64

	// AgentSpaces are the deployment's allowed agent spaces. A client names
	// one when it opens a workspace, and an undefined name is refused.
	AgentSpaces *worker.AgentSpaceSet

	// Workers supervises each session's worker process. Reconfiguring a
	// session stops its worker so the next turn starts a new generation in
	// the new agent space. A nil supervisor records the decision without
	// replacing anything.
	Workers *supervisor.Supervisor

	// LearnedDirectory is the global learned directory sessions load, which
	// the learned file endpoints list, read, and delete from. Empty leaves the
	// global scope out.
	LearnedDirectory string

	// SystemWorkspace is the private directory the system session runs in.
	// Empty means PEEN_SYSTEM_AGENT is off.
	SystemWorkspace string
}

// Registry is the control surface's session registry. It is the only place a
// client-requested workspace becomes a durable session, so the workspace policy
// cannot be bypassed by reaching the store directly.
type Registry struct {
	store           *session.Store
	policy          *WorkspacePolicy
	agentSpaces     *worker.AgentSpaceSet
	workers         *supervisor.Supervisor
	rootAgent       string
	defaultModel    string
	maxFileAPIBytes int64

	learnedDirectory string
	systemWorkspace  string
}

// OpenResult reports the session a workspace resolved to and whether this call
// is what created it.
type OpenResult struct {
	Session *models.Session
	Created bool
}

// NewRegistry validates the registry's dependencies.
func NewRegistry(options RegistryOptions) (*Registry, error) {
	if options.Store == nil || options.Policy == nil ||
		options.AgentSpaces == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"control registry dependency",
		)
	}

	if options.RootAgent == "" || options.DefaultModel == "" {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"control registry requires a root agent and default model",
		)
	}

	return &Registry{
		store:           options.Store,
		policy:          options.Policy,
		agentSpaces:     options.AgentSpaces,
		workers:         options.Workers,
		rootAgent:       options.RootAgent,
		defaultModel:    options.DefaultModel,
		maxFileAPIBytes: options.MaxFileAPIBytes,

		learnedDirectory: options.LearnedDirectory,
		systemWorkspace:  options.SystemWorkspace,
	}, nil
}

// Open resolves a requested workspace and returns the one durable session for
// it, creating that session the first time the directory is opened.
//
// The policy check runs before the store is touched, so a path outside every
// configured root is refused without creating a row. Opening the same directory
// again, by any of its names, resumes the existing session rather than making a
// second one. An empty agentSpaceName takes the session's existing space, and
// a different name is refused with
// session.AgentSpaceConflictError.
func (r *Registry) Open(
	ctx context.Context,
	requested string,
	agentSpaceName string,
) (OpenResult, error) {
	if err := r.checkRequestedAgentSpace(ctx, agentSpaceName); err != nil {
		return OpenResult{}, err
	}

	canonical, err := r.policy.Resolve(requested)
	if err != nil {
		// The requested path is deliberately absent from this record: it is
		// unvalidated client input, and the refusal reason is what matters.
		ctxscope.GetLogger(ctx).Warn(
			"workspace open refused",
			"reason", "workspace_policy",
			"err", err,
		)

		return OpenResult{}, ctxerrors.Wrap(err, "resolve requested workspace")
	}

	opened, err := r.store.OpenWorkspace(
		ctx,
		canonical,
		session.OpenSessionOptions{
			RootAgent:         r.rootAgent,
			ModelID:           r.defaultModel,
			AgentSpace:        agentSpaceName,
			DefaultAgentSpace: r.agentSpaces.Default(),
		},
	)
	if err != nil {
		if errors.Is(err, commerr.ErrConflict) {
			ctxscope.GetLogger(ctx).Warn(
				"workspace open refused",
				"reason", "agent_space_conflict",
				"err", err,
			)
		}

		return OpenResult{}, ctxerrors.Wrap(err, "open workspace session")
	}

	ctxscope.GetLogger(ctx).Info(
		"workspace session opened",
		"session_id", opened.Session.ID.String(),
		"workspace", opened.Session.Workspace,
		"agent_space", opened.Session.AgentSpace,
		"created", opened.Created,
	)

	return OpenResult{
		Session: opened.Session,
		Created: opened.Created,
	}, nil
}

// checkRequestedAgentSpace refuses an agent space the deployment does not
// offer. Naming an agent space is the only environment choice a client gets,
// and an undefined name is refused before any row exists. An empty name is
// left for the store, which gives the workspace's own agent space.
func (r *Registry) checkRequestedAgentSpace(
	ctx context.Context,
	agentSpaceName string,
) error {
	if agentSpaceName == "" {
		return nil
	}

	if _, err := r.agentSpaces.Select(agentSpaceName); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"workspace open refused",
			"reason", "agent_space_policy",
			"err", err,
		)

		return ctxerrors.Wrap(err, "select agent space")
	}

	return nil
}

// WorkspaceAgentSpace reports the existing session's agent space, if any.
func (r *Registry) WorkspaceAgentSpace(
	ctx context.Context,
	requested string,
) (string, bool, error) {
	canonical, err := r.policy.Resolve(requested)
	if err != nil {
		return "", false, ctxerrors.Wrap(err, "resolve requested workspace")
	}

	space, found, err := r.store.WorkspaceAgentSpace(ctx, canonical)
	if err != nil {
		return "", false, ctxerrors.Wrap(err, "read workspace agent space")
	}

	return space, found, nil
}

// Roots reports the directories this registry will open sessions under.
func (r *Registry) Roots() []string {
	return r.policy.Roots()
}
