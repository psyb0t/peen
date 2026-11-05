package session

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gorm/clause"
)

// emptyImagesJSON is images_json for a message without images.
const emptyImagesJSON = "[]"

// MessageImage is one image to store with a message.
type MessageImage struct {
	MediaType string `json:"mediaType"`
	Data      []byte `json:"data"`
}

// ImageDescription names an image's type and size, without its bytes, as
// the userMessage frames carry it.
type ImageDescription struct {
	MediaType string `json:"mediaType"`
	SizeBytes int    `json:"sizeBytes"`
}

// StoredImage is one images_json element. The bytes live in content_blobs
// under Blob as a JSON base64 string, so a history read carries only these
// references and an image repeated across messages is stored once.
type StoredImage struct {
	MediaType string `json:"mediaType"`
	SizeBytes int    `json:"sizeBytes"`
	Blob      string `json:"blob"`
}

// ImageBlob is how an image's bytes are stored in content_blobs: a JSON
// string of their base64 encoding, the form every content blob takes.
func ImageBlob(data []byte) []byte {
	return []byte(`"` + base64.StdEncoding.EncodeToString(data) + `"`)
}

// ImageBlobHash is the content_blobs key of an image's bytes.
func ImageBlobHash(data []byte) string {
	return contentblob.Hash(ImageBlob(data))
}

// DecodeImageBlob reads the bytes back from a stored image blob.
func DecodeImageBlob(blob []byte) ([]byte, error) {
	var data []byte
	if err := json.Unmarshal(blob, &data); err != nil {
		return nil, ctxerrors.Wrap(err, "decode image blob")
	}

	return data, nil
}

// DecodeStoredImages reads an images_json column. An empty column has no
// images.
func DecodeStoredImages(imagesJSON string) ([]StoredImage, error) {
	if imagesJSON == "" || imagesJSON == emptyImagesJSON {
		return nil, nil
	}

	var images []StoredImage
	if err := json.Unmarshal([]byte(imagesJSON), &images); err != nil {
		return nil, ctxerrors.Wrap(err, "decode stored message images")
	}

	return images, nil
}

// preparedImages is an images_json value and the content blobs it names,
// before the blobs are stored.
type preparedImages struct {
	json  string
	blobs []*models.ContentBlob
}

// prepareMessageImages builds the images_json that names images and the
// blobs that hold their bytes, without writing anything.
func prepareMessageImages(images []MessageImage) (preparedImages, error) {
	if len(images) == 0 {
		return preparedImages{json: emptyImagesJSON}, nil
	}

	stored := make([]StoredImage, 0, len(images))
	blobs := make([]*models.ContentBlob, 0, len(images))

	for _, image := range images {
		if image.MediaType == "" || len(image.Data) == 0 {
			return preparedImages{}, ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"message image needs a media type and data",
			)
		}

		data := ImageBlob(image.Data)
		hash := contentblob.Hash(data)
		blobs = append(blobs, &models.ContentBlob{Hash: hash, Data: data})
		stored = append(stored, StoredImage{
			MediaType: image.MediaType,
			SizeBytes: len(image.Data),
			Blob:      hash,
		})
	}

	encoded, err := json.Marshal(stored)
	if err != nil {
		return preparedImages{}, ctxerrors.Wrap(err,
			"encode message image list")
	}

	return preparedImages{json: string(encoded), blobs: blobs}, nil
}

// store writes the prepared blobs inside the caller's transaction. A blob
// already stored is kept.
func (p preparedImages) store(
	ctx context.Context,
	query *repositories.Query,
) error {
	if len(p.blobs) == 0 {
		return nil
	}

	if err := query.ContentBlob.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(p.blobs...); err != nil {
		return ctxerrors.Wrap(err, "store message images")
	}

	return nil
}

// encodeMessageImages stores images' bytes in content_blobs inside the
// caller's transaction and returns the images_json that names them.
func encodeMessageImages(
	ctx context.Context,
	query *repositories.Query,
	images []MessageImage,
) (string, error) {
	prepared, err := prepareMessageImages(images)
	if err != nil {
		return "", err
	}

	if err := prepared.store(ctx, query); err != nil {
		return "", err
	}

	return prepared.json, nil
}

// transcriptImagesJSON is the images_json a transcript row stores. A message
// whose images are already stored, such as a delivered inbox message, names
// them directly.
func transcriptImagesJSON(
	ctx context.Context,
	query *repositories.Query,
	input MessageInput,
) (string, error) {
	if len(input.Images) == 0 && input.StoredImagesJSON != "" {
		return input.StoredImagesJSON, nil
	}

	return encodeMessageImages(ctx, query, input.Images)
}

// imageDescriptions lists each stored image's type and size, for an event
// that describes a message without carrying its bytes.
func imageDescriptions(imagesJSON string) ([]ImageDescription, error) {
	stored, err := DecodeStoredImages(imagesJSON)
	if err != nil {
		return nil, err
	}

	if len(stored) == 0 {
		return nil, nil
	}

	described := make([]ImageDescription, 0, len(stored))
	for _, image := range stored {
		described = append(described, ImageDescription{
			MediaType: image.MediaType, SizeBytes: image.SizeBytes,
		})
	}

	return described, nil
}
