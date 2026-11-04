package agent

import (
	"errors"
	"net/http"
	"slices"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// imageMediaTypes are the image formats every provider driver accepts.
//
//nolint:gochecknoglobals // A fixed lookup table.
var imageMediaTypes = []string{
	elelem.MediaTypePNG,
	elelem.MediaTypeJPEG,
	elelem.MediaTypeGIF,
	elelem.MediaTypeWebP,
}

// imageRefusal joins an image sentinel with the validation class, so the
// transport reports it as a refused message with the sentinel's text.
func imageRefusal(sentinel error, detail string) error {
	return ctxerrors.Wrap(
		errors.Join(commerr.ErrValidationFailed, sentinel),
		detail,
	)
}

// validateImages checks a message's images against the deployment limits
// and checks that each one's bytes are the format its media type names.
func (r *Runtime) validateImages(images []MessageImage) error {
	if len(images) > r.maxImagesPerMessage {
		return imageRefusal(session.ErrTooManyImages, "message images")
	}

	for _, image := range images {
		if len(image.Data) > r.maxImageBytes {
			return imageRefusal(session.ErrImageTooLarge, "message image")
		}

		if !isImageOf(image.MediaType, image.Data) {
			return imageRefusal(session.ErrImageType, "message image")
		}
	}

	return nil
}

// isImageOf reports whether data sniffs as mediaType, one of the supported
// image formats.
func isImageOf(mediaType string, data []byte) bool {
	if !slices.Contains(imageMediaTypes, mediaType) || len(data) == 0 {
		return false
	}

	return http.DetectContentType(data) == mediaType
}

// modelAcceptsImages reports whether the model takes image input. An
// upstream's imageInput setting has already narrowed the capability.
func modelAcceptsImages(model ModelClient) bool {
	if model.Client == nil {
		return false
	}

	return model.Client.Capabilities(model.Model).SupportsImageInput
}

// requireImageInput refuses images for a model that does not take them.
func requireImageInput(model ModelClient, images []MessageImage) error {
	if len(images) == 0 || modelAcceptsImages(model) {
		return nil
	}

	return imageRefusal(session.ErrImagesNotAccepted, "message images")
}

// requireHistoryImageInput refuses a turn whose prompt replays images to a
// model that does not take them, such as a session moved to a text-only
// model. The images stay in the history; the turn needs a model that reads
// them.
func requireHistoryImageInput(model ModelClient, prompt elelem.Prompt) error {
	if modelAcceptsImages(model) || !messagesHaveImages(prompt.Messages()) {
		return nil
	}

	return imageRefusal(session.ErrHistoryImagesNotAccepted,
		"session history images")
}

func messagesHaveImages(messages []elelem.Message) bool {
	for _, message := range messages {
		for _, part := range message.Content {
			if part.Type == elelem.PartTypeImage {
				return true
			}
		}
	}

	return false
}
