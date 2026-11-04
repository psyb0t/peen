package session

import (
	"errors"

	"github.com/psyb0t/ctxerrors/commerr"
)

var (
	// ErrExecutionInboxPending requires continuation before successful finish.
	// Storage and transport return it joined with commerr.ErrConflict.
	ErrExecutionInboxPending = errors.New("execution inbox has pending work")
	// ErrSessionBusy reports a concurrent turn attempt for one session.
	ErrSessionBusy = commerr.ErrLockHeld
	// ErrInvalidPage reports invalid transcript pagination arguments.
	ErrInvalidPage = commerr.ErrValidationFailed

	// ErrRunningTurnSettingsChange reports a message sent while a turn runs
	// that asks for another model, reasoning level, or system prompt. A
	// queued message joins the running turn, so it cannot change how that
	// turn runs. It is always joined with commerr.ErrValidationFailed, and
	// its text is written for the person who sent the message.
	ErrRunningTurnSettingsChange = errors.New(
		"a message sent during a running turn cannot change its model, reasoning, or system prompt", //nolint:lll // One user-facing sentence.
	)

	// The image refusals below are always joined with
	// commerr.ErrValidationFailed, and their text is written for the person
	// who sent the message.

	// ErrImagesNotAccepted reports images sent to a model that does not take
	// image input, by its driver or by its upstream's imageInput setting.
	ErrImagesNotAccepted = errors.New("the model does not accept images")
	// ErrHistoryImagesNotAccepted reports a turn whose replayed history
	// carries images, run on a model that does not take image input.
	ErrHistoryImagesNotAccepted = errors.New(
		"the session history has images and the model does not accept images",
	)
	// ErrImagesNeedNewTurn reports images on a message that joins a running
	// turn or goes to a child agent.
	ErrImagesNeedNewTurn = errors.New(
		"only a message that starts a turn can carry images",
	)
	// ErrTooManyImages reports more images than PEEN_MAX_IMAGES_PER_MESSAGE.
	ErrTooManyImages = errors.New("the message carries too many images")
	// ErrImageTooLarge reports an image over PEEN_MAX_IMAGE_BYTES.
	ErrImageTooLarge = errors.New("an image is larger than the image limit")
	// ErrImageType reports an image whose bytes are not the PNG, JPEG, GIF, or
	// WebP its media type names.
	ErrImageType = errors.New(
		"an image is not PNG, JPEG, GIF, or WebP data matching its media type",
	)

	// ErrAgentRunFinished reports a message sent to a child agent that is no
	// longer running, so nothing would ever read it, or a non-terminal event
	// recorded after the run ended. It is always joined with
	// commerr.ErrConflict.
	ErrAgentRunFinished = errors.New("agent has finished")
	// ErrAgentRunNotFound reports a message sent to an agent run the session
	// never had. It is always joined with commerr.ErrNotFound.
	ErrAgentRunNotFound = errors.New("agent run not found")
	// ErrTurnLeaseInactive reports a write for a turn whose lease is already
	// released because the turn ended. It is always joined with
	// commerr.ErrInvalidState.
	ErrTurnLeaseInactive = errors.New("inactive turn lease")
)
