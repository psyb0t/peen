package control

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
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

// AdmissionWatcher reports when a session's worker admits a message.
//
// It is an interface so the router depends on hearing about admission rather
// than on how worker events reach the controller.
type AdmissionWatcher interface {
	WatchAdmission(
		sessionID uuid.UUID,
		requestID uuid.UUID,
		admitted func(),
	) func()
}

// TurnRouter sends one accepted client message to its session's worker.
//
// This is the join between the public control surface and the private worker
// protocol. The controller resolves which session the message is for and which
// operator-selected profile that session runs under, makes sure a worker for
// that profile is live, then hands the turn over. The controller never runs the
// model loop itself.
//
// Messages for one session reach its worker one at a time, in the order they
// were received. Each waits only until the worker has admitted the one before
// it, by starting a turn for it or queueing it, never for a whole turn.
type TurnRouter struct {
	registry        *Registry
	workers         WorkerSupervisor
	admissionEvents AdmissionWatcher
	admissions      *session.AdmissionGate
}

// NewTurnRouter binds the session registry to the worker supervisor.
// admissionEvents must observe the events the supervisor's workers publish,
// or each message would hold its session until its whole turn ended.
func NewTurnRouter(
	registry *Registry,
	workers WorkerSupervisor,
	admissionEvents AdmissionWatcher,
) (*TurnRouter, error) {
	if registry == nil || workers == nil || admissionEvents == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"turn router dependency",
		)
	}

	return &TurnRouter{
		registry:        registry,
		workers:         workers,
		admissionEvents: admissionEvents,
		admissions:      session.NewAdmissionGate(),
	}, nil
}

// ReserveSessionMessage takes a message's place in its session's admission
// line without waiting. A caller that runs the message on another goroutine
// reserves first, so messages are admitted in the order it received them
// rather than the order its goroutines run in. The reservation must reach
// RunReservedSessionMessage or be released.
func (r *TurnRouter) ReserveSessionMessage(
	sessionID uuid.UUID,
) *session.AdmissionTicket {
	return r.admissions.Reserve(sessionID)
}

// RunSessionMessage runs one message in the session's worker, reserving its
// place in the session's admission line on entry.
func (r *TurnRouter) RunSessionMessage(
	ctx context.Context,
	sessionID uuid.UUID,
	request agent.MessageRequest,
	requestID uuid.UUID,
) (*agent.MessageRunResult, error) {
	return r.RunReservedSessionMessage(
		ctx,
		r.ReserveSessionMessage(sessionID),
		request,
		requestID,
	)
}

// RunReservedSessionMessage runs one message in its session's worker once its
// reservation is admitted, and releases the reservation.
//
// The session is loaded first, so a message naming a session that does not
// exist fails before any worker is started. The profile comes from the stored
// session rather than the request: naming a profile is an operator decision
// recorded at open or reconfigure time, never something a message carries.
//
// The reservation is held while the worker is ensured, so concurrent messages
// cannot launch two workers for one session, and until the worker admits the
// message. The worker reports admission through the message's
// user_message.created event; a call that ends first, by failing or by
// answering, releases it too.
func (r *TurnRouter) RunReservedSessionMessage(
	ctx context.Context,
	reservation *session.AdmissionTicket,
	request agent.MessageRequest,
	requestID uuid.UUID,
) (*agent.MessageRunResult, error) {
	defer reservation.Release()

	// The worker would mint a request ID for a message without one, and the
	// router could then never recognise that message's admission.
	if requestID == uuid.Nil {
		requestID = uuid.New()
	}

	stored, err := r.registry.store.Get(ctx, reservation.SessionID())
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load the routed session")
	}

	if err := reservation.Wait(ctx); err != nil {
		return nil, ctxerrors.Wrap(err, "wait for the session to admit")
	}

	stopWatching := r.admissionEvents.WatchAdmission(
		stored.ID,
		requestID,
		reservation.Release,
	)
	defer stopWatching()

	live, err := r.workers.EnsureSessionWorker(
		ctx,
		stored.ID,
		stored.Workspace,
		stored.ExecutionProfile,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "ensure the session worker")
	}

	return runInWorker(ctx, live, stored, request, requestID)
}

// runInWorker hands one message to the session's live worker and waits for
// its answer, which for a queued message comes at once and for a started turn
// comes when the turn ends.
func runInWorker(
	ctx context.Context,
	live *protocol.Worker,
	stored *models.Session,
	request agent.MessageRequest,
	requestID uuid.UUID,
) (*agent.MessageRunResult, error) {
	ctxscope.GetLogger(ctx).Debug(
		"routing a turn to the session worker",
		"session_id", stored.ID.String(),
		"generation_id", live.GenerationID.String(),
		"execution_profile", stored.ExecutionProfile,
	)

	result, err := live.RunTurn(ctx, protocol.RunTurn{
		RequestID:       requestID,
		Message:         request.Message,
		Model:           optionalString(request.Model),
		ReasoningEffort: optionalString(request.ReasoningEffort),
		SystemPrompt:    promptContent(request),
		PromptMode:      promptMode(request),
		Origin:          turnOrigin(request),
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

func turnOrigin(request agent.MessageRequest) *protocol.TurnOrigin {
	if request.Origin == nil {
		return nil
	}

	return &protocol.TurnOrigin{
		EventID:   request.Origin.EventID,
		EventType: request.Origin.EventType,
	}
}
