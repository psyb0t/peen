package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type deferredRootContextKey struct{}

type deferredRootInput struct {
	inboxID uuid.UUID
	request TurnRequest
	skills  []skillMessage
}

type rootTurnAdmission struct {
	lease   session.Lease
	batch   events.Batch
	created bool
}

func deferredRootFromContext(ctx context.Context) *deferredRootInput {
	saved, _ := ctx.Value(deferredRootContextKey{}).(*deferredRootInput)

	return saved
}

func (r *Runtime) acquireRootTurn(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.StartTurnInput,
	batch events.Batch,
) (rootTurnAdmission, error) {
	saved := deferredRootFromContext(ctx)
	if saved == nil {
		lease, err := r.store.AcquireTurn(ctx, sessionID, input)
		if err != nil {
			return rootTurnAdmission{}, ctxerrors.Wrap(err, "acquire root turn")
		}

		return rootTurnAdmission{lease: lease, batch: batch, created: true}, nil
	}

	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return rootTurnAdmission{}, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"deferred turn admission storage")
	}

	admitted, err := store.AcquireDeferredExecutionTurn(ctx, sessionID,
		saved.inboxID, input)
	if err != nil {
		return rootTurnAdmission{}, ctxerrors.Wrap(err,
			"admit retained root input")
	}

	return rootTurnAdmission{
		lease: admitted.Lease, batch: sessionNoticeBatch(admitted.Notices),
		created: admitted.Created,
	}, nil
}

func (r *Runtime) prepareDeferredRoot(
	ctx context.Context,
	executionID, requestID uuid.UUID,
	sink EventSink,
) (*preparedTurn, error) {
	saved, err := r.loadDeferredRoot(ctx, executionID, requestID, sink)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load deferred root preparation")
	}

	ctx = context.WithValue(ctx, deferredRootContextKey{}, saved)

	prepared, err := r.prepareTurn(ctx, saved.request)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "prepare retained deferred input")
	}

	prepared.deferredInboxID = &saved.inboxID

	return prepared, nil
}

func (r *Runtime) loadDeferredRoot(
	ctx context.Context,
	executionID, requestID uuid.UUID,
	sink EventSink,
) (*deferredRootInput, error) {
	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"deferred root storage")
	}

	message, err := store.GetExecutionMessageByRequest(ctx, r.sessionID,
		executionID, requestID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read deferred root message")
	}

	if message.State != models.InboxMessageStateDeferred {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"deferred input is not available for another root")
	}

	source, err := store.GetExecution(ctx, r.sessionID, executionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read deferred source context")
	}

	return decodeDeferredRoot(source, message, sink)
}

func decodeDeferredRoot(
	source *models.Execution,
	message *models.ExecutionInboxMessage,
	sink EventSink,
) (*deferredRootInput, error) {
	if source == nil || message == nil {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"deferred root source or input is missing")
	}

	if source.Kind != models.ExecutionKindRoot ||
		source.State != models.ExecutionStateCompleted {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"deferred root source has not completed")
	}

	var conversation workflowConversation
	if err := json.Unmarshal([]byte(source.ContextJSON),
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode deferred source context")
	}

	if err := validateDeferredRootIdentity(source, message,
		conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "validate deferred source context")
	}

	skills, err := decodeDeferredSkills(message.PartsJSON)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore deferred skill content")
	}

	request := conversation.Root.request(sink)
	request.RequestID = message.RequestID
	request.Message = message.Message
	request.Workspace = message.WorkingDirectory
	request.Model = conversation.ModelReference
	request.ReasoningEffort = conversation.ReasoningEffort

	request.SourceEventID = uuid.Nil
	if message.SourceEventID != nil {
		request.SourceEventID = *message.SourceEventID
	}

	request.Origin = nil
	request.ExpectedStopRevision = &message.StopRevision

	return &deferredRootInput{
		inboxID: message.ID, request: request,
		skills: skills,
	}, nil
}

func validateDeferredRootIdentity(
	source *models.Execution,
	message *models.ExecutionInboxMessage,
	conversation workflowConversation,
) error {
	root := conversation.Root
	if !root.hasSupportedVersion() || root.SessionID != message.SessionID ||
		root.TurnID != message.TurnID || source.ID != message.ExecutionID ||
		source.SessionID != message.SessionID ||
		source.RootTurnID != message.TurnID ||
		conversation.ModelReference == "" {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"deferred source identity or model differs")
	}

	return nil
}

func decodeDeferredSkills(encoded string) ([]skillMessage, error) {
	var parts []session.ExecutionInboxPart
	if err := json.Unmarshal([]byte(encoded), &parts); err != nil {
		return nil, ctxerrors.Wrap(err, "decode deferred skill parts")
	}

	skills := make([]skillMessage, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.SkillName) == "" || part.Content == "" {
			return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
				"deferred skill content is incomplete")
		}

		skills = append(skills, skillMessage{
			name: part.SkillName,
			text: part.Content, isNote: part.SkillIsNote,
		})
	}

	return skills, nil
}

func validateRootSkillReferences(
	ctx context.Context,
	snapshot harness.Snapshot,
	input TurnRequest,
) error {
	if saved := deferredRootFromContext(ctx); saved != nil {
		return validateDeferredRequest(saved, input)
	}

	return requireKnownSkillReferences(snapshot, input.Message)
}

func rootInputSkills(
	ctx context.Context,
	snapshot harness.Snapshot,
	ledger *skillLedger,
	input TurnRequest,
) ([]skillMessage, error) {
	if saved := deferredRootFromContext(ctx); saved != nil {
		if err := validateDeferredRequest(saved, input); err != nil {
			return nil, ctxerrors.Wrap(err, "validate frozen root skills")
		}

		return slices.Clone(saved.skills), nil
	}

	return buildSkillMessages(ctx, snapshot, ledger, input.Message)
}

func validateDeferredRequest(
	saved *deferredRootInput,
	input TurnRequest,
) error {
	if saved.inboxID == uuid.Nil || input.SessionID == nil ||
		saved.request.SessionID == nil ||
		*input.SessionID != *saved.request.SessionID ||
		input.RequestID != saved.request.RequestID ||
		input.Message != saved.request.Message {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"frozen input belongs to another root request")
	}

	return nil
}
