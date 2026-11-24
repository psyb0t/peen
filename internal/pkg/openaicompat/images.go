package openaicompat

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strings"

	"github.com/psyb0t/peen/internal/pkg/agent"
)

func (h handler) decodeImage(value string) (
	agent.MessageImage, *responseError,
) {
	image := agent.MessageImage{}
	problem := invalidField("messages.content.image_url")

	mediaType, encoded, valid := imageDataURLParts(value,
		h.dependencies.MaxImageBytes)
	if !valid {
		return image, &problem
	}

	if !slices.Contains([]string{
		mediaTypePNG, mediaTypeJPEG, mediaTypeGIF, mediaTypeWebP,
	}, mediaType) {
		return image, &problem
	}

	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 ||
		len(data) > h.dependencies.MaxImageBytes ||
		http.DetectContentType(data) != mediaType {
		return image, &problem
	}

	return agent.MessageImage{MediaType: mediaType, Data: data}, nil
}

func imageDataURLParts(value string, maximum int) (string, string, bool) {
	metadata, encoded, found := strings.Cut(value, ",")
	if !found || !strings.HasPrefix(metadata, dataURLPrefix) ||
		!strings.HasSuffix(metadata, base64Suffix) ||
		strings.ContainsAny(encoded, "\r\n") ||
		len(encoded) > base64.StdEncoding.EncodedLen(maximum) {
		return "", "", false
	}

	mediaType := strings.TrimSuffix(strings.TrimPrefix(metadata,
		dataURLPrefix), base64Suffix)

	return mediaType, encoded, true
}
