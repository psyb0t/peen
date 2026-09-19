package control

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
)

// WorkerSupervisor is the worker lifecycle the router needs.
//
// It is an interface so the router depends on ensuring and cancelling a
// session's worker rather than on how one is launched.
type WorkerSupervisor interface {
	EnsureSessionWorker(
		ctx context.Context,
		sessionID uuid.UUID,
		workspace string,
		profileName string,
	) (*protocol.Worker, error)
	Current(sessionID uuid.UUID) (*protocol.Worker, bool)
}

// TurnRouter sends one accepted client message to its session's worker.
//
// This is the join between the public control surface and the private worker
// protocol. The controller resolves which session the message is for and which
// operator-selected profile that session runs under, makes sure a worker for
// that profile is live, then hands the turn over. The controller never runs the
// model loop itself.
type TurnRouter struct {
	registry *Registry
	workers  WorkerSupervisor
}

// NewTurnRouter binds the session registry to the worker supervisor.
func NewTurnRouter(
	registry *Registry,
	workers WorkerSupervisor,
) (*TurnRouter, error) {
	if registry == nil || workers == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"turn router dependency",
		)
	}

	return &TurnRouter{registry: registry, workers: workers}, nil
}

// RunSessionMessage runs one turn in the session's worker.
//
// The session is loaded first, so a message naming a session that does not
// exist fails before any worker is started. The profile comes from the stored
// session rather than the request: naming a profile is an operator decision
// recorded at open or reconfigure time, never something a message carries.
func (r *TurnRouter) RunSessionMessage(
	ctx context.Context,
	sessionID uuid.UUID,
	request agent.MessageRequest,
	requestID uuid.UUID,
) (*agent.MessageRunResult, error) {
	stored, err := r.registry.store.Get(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load the routed session")
	}

	live, err := r.workers.EnsureSessionWorker(
		ctx,
		stored.ID,
		stored.Workspace,
		stored.ExecutionProfile,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "ensure the session worker")
	}

	ctxscope.GetLogger(ctx).Debug(
		"routing a turn to the session worker",
		"session_id", stored.ID.String(),
		"generation_id", live.GenerationID.String(),
		"execution_profile", stored.ExecutionProfile,
	)

	result, err := live.RunTurn(ctx, protocol.RunTurn{
		RequestID:    requestID,
		Message:      request.Message,
		Model:        optionalString(request.Model),
		SystemPrompt: promptContent(request),
		PromptMode:   promptMode(request),
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "run the turn in the session worker")
	}

	return &agent.MessageRunResult{
		SessionID: stored.ID,
		Queued:    result.Queued,
		Text:      result.Text,
	}, nil
}

// SignalSessionJob asks the session's live worker to signal one of its jobs.
//
// A job's process group is a child of the worker, so the controller cannot
// reach it. A nil response means no live worker holds the job, which tells the
// caller to record the request against the durable row instead of treating it
// as an error.
func (r *TurnRouter) SignalSessionJob(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	signal string,
) (*api.JobSignalResponse, error) {
	live, found := r.workers.Current(sessionID)
	if !found {
		return nil, nil //nolint:nilnil // No live worker is not a result.
	}

	result, err := live.SignalJob(ctx, jobID, signal)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "signal the session worker job")
	}

	if !result.Handled {
		return nil, nil //nolint:nilnil // The worker no longer holds the job.
	}

	return &api.JobSignalResponse{
		JobId:     jobID,
		Signalled: result.Signalled,
		State:     api.JobSignalResponseState(result.State),
	}, nil
}

// CancelSessionTurn asks the session's live worker to cancel its active turn.
//
// A session with no live worker has nothing running in this controller, so the
// answer is that nothing was cancelled rather than an error.
func (r *TurnRouter) CancelSessionTurn(
	ctx context.Context,
	sessionID uuid.UUID,
) (bool, error) {
	live, found := r.workers.Current(sessionID)
	if !found {
		return false, nil
	}

	cancelled, err := live.Cancel(ctx)
	if err != nil {
		return false, ctxerrors.Wrap(err, "cancel the session worker turn")
	}

	return cancelled, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

func promptContent(request agent.MessageRequest) string {
	if request.SystemPrompt == nil {
		return ""
	}

	return request.SystemPrompt.Content
}

func promptMode(request agent.MessageRequest) string {
	if request.SystemPrompt == nil {
		return ""
	}

	return string(request.SystemPrompt.Mode)
}
