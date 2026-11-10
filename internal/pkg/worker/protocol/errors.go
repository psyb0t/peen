package protocol

import (
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/publicerror"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// ErrWorkerRejected reports a registration the controller refused. A worker
// that sees it must exit rather than retry: the credential, session, or
// generation it was launched with is not one the controller will accept.
var ErrWorkerRejected = errors.New("worker registration rejected")

// Code is the transport-neutral error class carried on a result frame.
//
// It exists so a failure keeps its meaning across the socket. Without it a
// worker would see every controller refusal as one opaque string and could not
// tell a busy session from a missing record.
type Code string

const (
	CodeUnknown          Code = "unknown"
	CodeNotFound         Code = "notFound"
	CodeAlreadyExists    Code = "alreadyExists"
	CodeConflict         Code = "conflict"
	CodeInvalidState     Code = "invalidState"
	CodeValidation       Code = "validationFailed"
	CodePermissionDenied Code = "permissionDenied"
	CodeLockHeld         Code = "lockHeld"
	CodeCancelled        Code = "cancelled"
	CodeNotImplemented   Code = "notImplemented"

	CodeRunningTurnSettingsChange Code = "runningTurnSettingsChange"
	CodeUserMessageQueueFull      Code = "userMessageQueueFull"
	CodeUnknownSkill              Code = "unknownSkill"
	CodeAgentRunFinished          Code = "agentRunFinished"
	CodeAgentRunNotFound          Code = "agentRunNotFound"
	CodeExecutionInboxPending     Code = "executionInboxPending"
	CodePromptBudgetExceeded      Code = "promptBudgetExceeded"
	CodeImagesNotAccepted         Code = "imagesNotAccepted"
	CodeHistoryImagesNotAccepted  Code = "historyImagesNotAccepted"
	CodeImagesNeedNewTurn         Code = "imagesNeedNewTurn"
	CodeTooManyImages             Code = "tooManyImages"
	CodeImageTooLarge             Code = "imageTooLarge"
	CodeImageType                 Code = "imageType"
	CodeMessageTooLarge           Code = "messageTooLarge"
	CodeSystemPromptTooLarge      Code = "systemPromptTooLarge"
	CodeInvalidInstruction        Code = "invalidInstruction"
	CodeInvalidSkill              Code = "invalidSkill"
	CodeInvalidAgent              Code = "invalidAgent"
	CodeInvalidEventHandler       Code = "invalidEventHandler"
	CodeInvalidHook               Code = "invalidHook"
	CodeHarnessResourceLimit      Code = "harnessResourceLimit"
	CodeUnreadableHarnessLayer    Code = "unreadableHarnessLayer"
	CodeTurnLeaseInactive         Code = "turnLeaseInactive"
	CodeTurnRoundLimit            Code = "turnRoundLimit"
	CodeTurnTokenLimit            Code = "turnTokenLimit"
)

// Error is one failure as it crosses the socket.
type Error struct {
	Code Code `json:"code"`
	// Message is operator-facing text. It never carries a credential, because
	// the controller only ever puts its own wrap messages here.
	Message string `json:"message"`
}

// sentinels maps each transport code to the shared sentinel it stands for.
//
//nolint:gochecknoglobals // A lookup table both directions read.
var sentinels = map[Code]error{
	CodeNotFound:         commerr.ErrNotFound,
	CodeAlreadyExists:    commerr.ErrAlreadyExists,
	CodeConflict:         commerr.ErrConflict,
	CodeInvalidState:     commerr.ErrInvalidState,
	CodeValidation:       commerr.ErrValidationFailed,
	CodePermissionDenied: commerr.ErrPermissionDenied,
	CodeLockHeld:         commerr.ErrLockHeld,
	CodeCancelled:        commerr.ErrCancelled,
	CodeNotImplemented:   commerr.ErrNotImplemented,
}

// detailedFailure is a failure more specific than its shared class. The
// controller tells a person exactly why their message was refused, so the
// detail has to survive the socket rather than collapse into the class.
type detailedFailure struct {
	code   Code
	detail error
	class  error
	// public marks a failure whose error carries its own client-safe text,
	// which crosses the socket in place of the wrapped chain.
	public bool
}

// detailedFailures is checked before sentinels, because an error carrying a
// detail also matches its class.
//
//nolint:gochecknoglobals // A lookup table both directions read.
var detailedFailures = []detailedFailure{
	{
		code:   CodeExecutionInboxPending,
		detail: session.ErrExecutionInboxPending,
		class:  commerr.ErrConflict,
	},
	{
		code:   CodeRunningTurnSettingsChange,
		detail: session.ErrRunningTurnSettingsChange,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeUserMessageQueueFull,
		detail: elelem.ErrUserMessageQueueFull,
		class:  commerr.ErrConflict,
	},
	{
		code:   CodeAgentRunFinished,
		detail: session.ErrAgentRunFinished,
		class:  commerr.ErrConflict,
	},
	{
		code:   CodeAgentRunNotFound,
		detail: session.ErrAgentRunNotFound,
		class:  commerr.ErrNotFound,
	},
	{
		code:   CodePromptBudgetExceeded,
		detail: harness.ErrPromptBudgetExceeded,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeImagesNotAccepted,
		detail: session.ErrImagesNotAccepted,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeHistoryImagesNotAccepted,
		detail: session.ErrHistoryImagesNotAccepted,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeImagesNeedNewTurn,
		detail: session.ErrImagesNeedNewTurn,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeTooManyImages,
		detail: session.ErrTooManyImages,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeImageTooLarge,
		detail: session.ErrImageTooLarge,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeTurnLeaseInactive,
		detail: session.ErrTurnLeaseInactive,
		class:  commerr.ErrInvalidState,
	},
	// A turn that hit its round or token limit reports the limit's own text,
	// which names the setting that raised it, without the wrapped chain.
	{
		code:   CodeTurnRoundLimit,
		detail: session.ErrTurnRoundLimit,
		class:  commerr.ErrOutOfRange,
		public: true,
	},
	{
		code:   CodeTurnTokenLimit,
		detail: session.ErrTurnTokenLimit,
		class:  commerr.ErrOutOfRange,
		public: true,
	},
	{
		code:   CodeImageType,
		detail: session.ErrImageType,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeMessageTooLarge,
		detail: session.ErrMessageTooLarge,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeSystemPromptTooLarge,
		detail: session.ErrSystemPromptTooLarge,
		class:  commerr.ErrValidationFailed,
	},
	// A harness the worker resolves can refuse a turn. The controller
	// reports each refusal as HARNESS_CONFIGURATION_INVALID with its reason,
	// so the detail has to reach it.
	{
		code:   CodeInvalidInstruction,
		detail: harness.ErrInvalidInstruction,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeInvalidSkill,
		detail: harness.ErrInvalidSkill,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeInvalidAgent,
		detail: harness.ErrInvalidAgent,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeInvalidEventHandler,
		detail: harness.ErrInvalidEventHandler,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeInvalidHook,
		detail: harness.ErrInvalidHook,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeHarnessResourceLimit,
		detail: harness.ErrResourceLimit,
		class:  commerr.ErrValidationFailed,
	},
	{
		code:   CodeUnreadableHarnessLayer,
		detail: harness.ErrUnreadableLayer,
		class:  commerr.ErrValidationFailed,
	},
}

// codeFor classifies an error for the wire. A detailed failure wins over its
// class. Otherwise each check is exclusive in practice; an unrecognised
// failure stays unknown rather than being forced into a class it does not
// belong to.
func codeFor(err error) Code {
	var unknown *harness.UnknownSkillError
	if errors.As(err, &unknown) {
		return CodeUnknownSkill
	}

	for _, failure := range detailedFailures {
		if errors.Is(err, failure.detail) {
			return failure.code
		}
	}

	for code, sentinel := range sentinels {
		if errors.Is(err, sentinel) {
			return code
		}
	}

	return CodeUnknown
}

// NewError converts a controller-side failure into a wire error.
func NewError(err error) *Error {
	if err == nil {
		return nil
	}

	// An unknown skill carries a message written for the sender, without the
	// wrap locations err.Error() adds, and the sender needs it intact.
	var unknown *harness.UnknownSkillError
	if errors.As(err, &unknown) {
		return &Error{Code: CodeUnknownSkill, Message: unknown.Message}
	}

	code := codeFor(err)

	message := err.Error()
	if failure, found := detailedFailureFor(code); found && failure.public {
		message = publicerror.Message(err)
	}

	return &Error{Code: code, Message: message}
}

func detailedFailureFor(code Code) (detailedFailure, bool) {
	for _, failure := range detailedFailures {
		if failure.code == code {
			return failure, true
		}
	}

	return detailedFailure{}, false
}

// Unwrap rebuilds a Go error from a wire error, restoring the sentinel so
// errors.Is still answers correctly on the receiving side. A detailed failure
// comes back matching both its detail and its class.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}

	if e.Code == CodeUnknownSkill {
		return ctxerrors.Wrap(
			errors.Join(
				commerr.ErrValidationFailed,
				&harness.UnknownSkillError{Message: e.Message},
			),
			"unknown skill reference",
		)
	}

	if failure, found := detailedFailureFor(e.Code); found {
		restored := errors.Join(failure.class, failure.detail)
		if failure.public {
			return publicerror.WithMessage(e.Message, restored)
		}

		return ctxerrors.Wrap(restored, e.Message)
	}

	sentinel, found := sentinels[e.Code]
	if !found {
		return ctxerrors.New(e.Message)
	}

	return ctxerrors.Wrap(sentinel, e.Message)
}
