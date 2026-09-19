package control

import (
	"context"

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
	Store        *session.Store
	Policy       *WorkspacePolicy
	RootAgent    string
	DefaultModel string

	// Profiles are the deployment's allowed execution profiles. A client names
	// one when it opens a workspace, and an undefined name is refused.
	Profiles *worker.ProfileSet

	// Workers supervises each session's worker process. Reconfiguring a
	// session stops its worker so the next turn starts a new generation under
	// the new profile. A nil supervisor records the decision without replacing
	// anything.
	Workers *supervisor.Supervisor
}

// Registry is the control surface's session registry. It is the only place a
// client-requested workspace becomes a durable session, so the workspace policy
// cannot be bypassed by reaching the store directly.
type Registry struct {
	store        *session.Store
	policy       *WorkspacePolicy
	profiles     *worker.ProfileSet
	workers      *supervisor.Supervisor
	rootAgent    string
	defaultModel string
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
		options.Profiles == nil {
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
		store:        options.Store,
		policy:       options.Policy,
		profiles:     options.Profiles,
		workers:      options.Workers,
		rootAgent:    options.RootAgent,
		defaultModel: options.DefaultModel,
	}, nil
}

// Open resolves a requested workspace and returns the one durable session for
// it, creating that session the first time the directory is opened.
//
// The policy check runs before the store is touched, so a path outside every
// configured root is refused without creating a row. Opening the same directory
// again, by any of its names, resumes the existing session rather than making a
// second one.
func (r *Registry) Open(
	ctx context.Context,
	requested string,
	profileName string,
) (OpenResult, error) {
	// Naming a profile is the only environment choice a client gets, and an
	// undefined name is refused before any row exists.
	profile, err := r.profiles.Select(profileName)
	if err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"workspace open refused",
			"reason", "execution_profile_policy",
			"err", err,
		)

		return OpenResult{}, ctxerrors.Wrap(err, "select execution profile")
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
			RootAgent: r.rootAgent,
			ModelID:   r.defaultModel,
			// Only a newly created session takes this. Opening an existing
			// session never silently changes the profile it already runs
			// under.
			ExecutionProfile: profile.Name,
		},
	)
	if err != nil {
		return OpenResult{}, ctxerrors.Wrap(err, "open workspace session")
	}

	ctxscope.GetLogger(ctx).Info(
		"workspace session opened",
		"session_id", opened.Session.ID.String(),
		"workspace", opened.Session.Workspace,
		"execution_profile", opened.Session.ExecutionProfile,
		"created", opened.Created,
	)

	return OpenResult{
		Session: opened.Session,
		Created: opened.Created,
	}, nil
}

// Roots reports the directories this registry will open sessions under.
func (r *Registry) Roots() []string {
	return r.policy.Roots()
}
