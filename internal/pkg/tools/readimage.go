package tools

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

// imageExtensions maps the file extensions readFile reads as images to their
// media types, the formats every provider driver accepts.
//
//nolint:gochecknoglobals // A fixed lookup table.
var imageExtensions = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// imageMediaTypeForPath returns the media type of an image file name, or ""
// when readFile reads the file as text.
func imageMediaTypeForPath(path string) string {
	return imageExtensions[strings.ToLower(filepath.Ext(path))]
}

// readImageFile reads an image file whole, bounded by MaxImageBytes, and
// refuses one whose bytes are not the format its extension names.
func (e *Executor) readImageFile(
	resolved string,
	mediaType string,
) (ReadFileOutput, error) {
	content, _, err := e.readRegularFile(
		resolved,
		int64(e.Limits().MaxImageBytes),
	)
	if err != nil {
		return ReadFileOutput{}, ctxerrors.Wrap(err, "read image file")
	}

	if http.DetectContentType(content) != mediaType {
		return ReadFileOutput{}, ctxerrors.Wrapf(
			ErrImageContent,
			"%s is not %s data",
			resolved,
			mediaType,
		)
	}

	sha := hashBytes(content)
	e.observeContent(resolved, sha)

	return ReadFileOutput{
		Path:      resolved,
		SHA256:    sha,
		MediaType: mediaType,
		SizeBytes: len(content),
		Image:     content,
	}, nil
}
