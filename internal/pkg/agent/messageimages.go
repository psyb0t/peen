package agent

import (
	"context"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	// historyImageBatchBytes bounds the image bytes one blob read fetches, so
	// a history with many images never needs one oversized worker frame.
	historyImageBatchBytes = 8 << 20

	// imageTokenEstimate is what one image costs in the context budget.
	// Providers bill an image by its pixel size, roughly 1,000 to 1,600
	// tokens for a screenshot, and the text tokenizers count none of it.
	imageTokenEstimate = 1600

	// imageReferencePrefix starts the URL of an image part whose bytes stay
	// in content_blobs. The rest of the URL is the blob hash. It never
	// reaches a provider.
	imageReferencePrefix = "peen-blob:"
)

// imageTokenCounter adds imageTokenEstimate per image part to a text
// counter, so compaction makes room for images as it does for text.
type imageTokenCounter struct {
	text elelem.TokenCounter
}

// withImageTokens wraps counter so images count against the context budget.
//
//nolint:ireturn // Elelem exposes token counting as an interface dependency.
func withImageTokens(counter elelem.TokenCounter) elelem.TokenCounter {
	if counter == nil {
		counter = elelem.DefaultTokenCounter()
	}

	return imageTokenCounter{text: counter}
}

func (c imageTokenCounter) Count(
	messages []elelem.Message,
	tools []elelem.Tool,
) (int, error) {
	tokens, err := c.text.Count(messages, tools)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count message text")
	}

	for _, message := range messages {
		for _, part := range message.Content {
			if part.Type == elelem.PartTypeImage {
				tokens += imageTokenEstimate
			}
		}
	}

	return tokens, nil
}

// imageBytes maps a stored image's blob hash to its raw bytes.
type imageBytes map[string][]byte

// imageBlobReader reads stored image bytes by their content blob hash.
type imageBlobReader interface {
	GetContentBlobs(
		ctx context.Context,
		hashes []string,
	) (map[string][]byte, error)
}

// savedHistory is the history a workflow context keeps: directoryHistory
// with each image part naming its content blob instead of holding its bytes.
// The context is saved after every round and crosses the worker socket each
// time, so images stay in content_blobs, stored once, and
// resolveImageReferences reads them back right before a model call.
func savedHistory(messages []elelem.Message) []elelem.Message {
	history := directoryHistory(messages)
	history = slices.DeleteFunc(history, isFileRecoveryMessage)

	for index, message := range history {
		history[index].ToolCalls = validToolCalls(message.ToolCalls)

		if !hasImageBytes(message) {
			continue
		}

		content := slices.Clone(message.Content)
		for partIndex, part := range content {
			content[partIndex] = imageReferencePart(part)
		}

		history[index].Content = content
	}

	return history
}

func hasImageBytes(message elelem.Message) bool {
	for _, part := range message.Content {
		if part.Type == elelem.PartTypeImage && part.Image != nil &&
			len(part.Image.Data) > 0 {
			return true
		}
	}

	return false
}

// imageReferencePart replaces an image part's bytes with a reference to the
// content blob that holds them. Any other part is returned as it is.
func imageReferencePart(part elelem.Part) elelem.Part {
	if part.Type != elelem.PartTypeImage || part.Image == nil ||
		len(part.Image.Data) == 0 {
		return part
	}

	source := *part.Image
	source.URL = imageReferencePrefix + session.ImageBlobHash(source.Data)
	source.Data = nil
	part.Image = &source

	return part
}

// imageReferenceHash returns the blob hash an image part names, or "" for a
// part that is not an image reference.
func imageReferenceHash(part elelem.Part) string {
	if part.Type != elelem.PartTypeImage || part.Image == nil {
		return ""
	}

	hash, isReference := strings.CutPrefix(part.Image.URL,
		imageReferencePrefix)
	if !isReference {
		return ""
	}

	return hash
}

// resolveImageReferences returns messages with every image reference
// replaced by the image bytes from content_blobs. Each image is read in its
// own request, because one image may come close to the worker frame limit.
func resolveImageReferences(
	ctx context.Context,
	store imageBlobReader,
	messages []elelem.Message,
) ([]elelem.Message, error) {
	loaded := imageBytes{}
	resolved := slices.Clone(messages)

	for index, message := range resolved {
		var content elelem.Content

		for partIndex, part := range message.Content {
			hash := imageReferenceHash(part)
			if hash == "" {
				continue
			}

			data, err := loadImageBlob(ctx, store, loaded, hash)
			if err != nil {
				return nil, err
			}

			if content == nil {
				content = slices.Clone(message.Content)
			}

			source := *part.Image
			source.URL, source.Data = "", data
			content[partIndex].Image = &source
		}

		if content != nil {
			resolved[index].Content = content
		}
	}

	return resolved, nil
}

func loadImageBlob(
	ctx context.Context,
	store imageBlobReader,
	loaded imageBytes,
	hash string,
) ([]byte, error) {
	if data, ok := loaded[hash]; ok {
		return data, nil
	}

	found, err := store.GetContentBlobs(ctx, []string{hash})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read referenced image")
	}

	raw, ok := found[hash]
	if !ok {
		return nil, ctxerrors.Wrapf(commerr.ErrNotFound,
			"referenced image %s", hash)
	}

	data, err := session.DecodeImageBlob(raw)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "decode referenced image")
	}

	loaded[hash] = data

	return data, nil
}

// loadMessageImages fetches the images the given images_json columns name.
// Reads are batched by size, and an image several messages share is read
// once.
func loadMessageImages(
	ctx context.Context,
	store imageBlobReader,
	columns []string,
) (imageBytes, error) {
	batches, err := imageBlobBatches(columns)
	if err != nil {
		return nil, err
	}

	loaded := imageBytes{}

	for _, batch := range batches {
		found, err := store.GetContentBlobs(ctx, batch)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read message images")
		}

		for _, hash := range batch {
			raw, ok := found[hash]
			if !ok {
				return nil, ctxerrors.Wrapf(commerr.ErrNotFound,
					"message image %s", hash)
			}

			data, err := session.DecodeImageBlob(raw)
			if err != nil {
				return nil, ctxerrors.Wrap(err, "decode message image")
			}

			loaded[hash] = data
		}
	}

	return loaded, nil
}

func imageBlobBatches(columns []string) ([][]string, error) {
	var (
		batches      [][]string
		current      []string
		currentBytes int
	)

	seen := map[string]bool{}

	for _, column := range columns {
		images, err := session.DecodeStoredImages(column)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "list history images")
		}

		for _, image := range images {
			if seen[image.Blob] {
				continue
			}

			seen[image.Blob] = true

			if len(current) > 0 &&
				currentBytes+image.SizeBytes > historyImageBatchBytes {
				batches = append(batches, current)
				current, currentBytes = nil, 0
			}

			current = append(current, image.Blob)
			currentBytes += image.SizeBytes
		}
	}

	if len(current) > 0 {
		batches = append(batches, current)
	}

	return batches, nil
}

func messageImageColumns(messages []*models.Message) []string {
	columns := make([]string, 0, len(messages))
	for _, message := range messages {
		if message != nil {
			columns = append(columns, message.ImagesJSON)
		}
	}

	return columns
}

func agentRunMessageImageColumns(messages []*models.AgentRunMessage) []string {
	columns := make([]string, 0, len(messages))
	for _, message := range messages {
		if message != nil {
			columns = append(columns, message.ImagesJSON)
		}
	}

	return columns
}

// messageContent is a stored message's text with its images after it.
// images must hold every blob imagesJSON names.
func messageContent(
	text, imagesJSON string,
	images imageBytes,
) (elelem.Content, error) {
	stored, err := session.DecodeStoredImages(imagesJSON)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read message images")
	}

	resolved := make([]session.MessageImage, 0, len(stored))

	for _, image := range stored {
		data, ok := images[image.Blob]
		if !ok {
			return nil, ctxerrors.Wrapf(commerr.ErrInvalidState,
				"message image %s was not loaded", image.Blob)
		}

		resolved = append(resolved, session.MessageImage{
			MediaType: image.MediaType, Data: data,
		})
	}

	return contentWithImages(text, resolved), nil
}

// loadStoredImages reads back the bytes of every image an images_json value
// names, in order.
func loadStoredImages(
	ctx context.Context,
	store imageBlobReader,
	imagesJSON string,
) ([]MessageImage, error) {
	stored, err := session.DecodeStoredImages(imagesJSON)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read stored image list")
	}

	if len(stored) == 0 {
		return nil, nil
	}

	hashes := make([]string, 0, len(stored))
	for _, image := range stored {
		hashes = append(hashes, image.Blob)
	}

	blobs, err := store.GetContentBlobs(ctx, hashes)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read stored image blobs")
	}

	images := make([]MessageImage, 0, len(stored))

	for _, image := range stored {
		blob, ok := blobs[image.Blob]
		if !ok {
			return nil, ctxerrors.Wrapf(commerr.ErrNotFound,
				"stored image %s", image.Blob)
		}

		data, err := session.DecodeImageBlob(blob)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "decode stored image")
		}

		images = append(images, MessageImage{
			MediaType: image.MediaType, Data: data,
		})
	}

	return images, nil
}

// storedReferenceContent is text followed by one image part per stored
// image, each naming its content blob instead of carrying bytes. A model
// round reads the bytes back before it sends the request.
func storedReferenceContent(
	text, imagesJSON string,
) (elelem.Content, error) {
	stored, err := session.DecodeStoredImages(imagesJSON)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read stored message images")
	}

	if len(stored) == 0 {
		return elelem.Text(text), nil
	}

	parts := make(elelem.Content, 0, len(stored)+1)
	if text != "" {
		parts = append(parts, elelem.TextOf(text))
	}

	for _, image := range stored {
		parts = append(parts, elelem.Part{
			Type: elelem.PartTypeImage,
			Image: &elelem.ImageSource{
				URL:       imageReferencePrefix + image.Blob,
				MediaType: image.MediaType,
			},
		})
	}

	return parts, nil
}

// contentWithImages is text followed by one image part per image. Text alone
// stays a single text part, as every message without images is.
func contentWithImages(
	text string,
	images []session.MessageImage,
) elelem.Content {
	if len(images) == 0 {
		return elelem.Text(text)
	}

	parts := make(elelem.Content, 0, len(images)+1)
	if text != "" {
		parts = append(parts, elelem.TextOf(text))
	}

	for _, image := range images {
		parts = append(parts, elelem.ImageBytes(image.Data, image.MediaType))
	}

	return parts
}
