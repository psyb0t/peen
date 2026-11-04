package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPNGBytes starts with the PNG signature, which is all content sniffing
// reads.
const testPNGBytes = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR test image"

type mapImageBlobs map[string][]byte

func (m mapImageBlobs) GetContentBlobs(
	_ context.Context,
	hashes []string,
) (map[string][]byte, error) {
	found := map[string][]byte{}
	for _, hash := range hashes {
		if blob, ok := m[hash]; ok {
			found[hash] = blob
		}
	}

	return found, nil
}

func storedBlobs(images ...[]byte) mapImageBlobs {
	blobs := mapImageBlobs{}
	for _, data := range images {
		blobs[session.ImageBlobHash(data)] = session.ImageBlob(data)
	}

	return blobs
}

func imageModel(t *testing.T, acceptsImages bool) ModelClient {
	t.Helper()

	driver := elelemtest.NewScriptedDriver().WithCapabilities(
		elelem.Capabilities{SupportsImageInput: acceptsImages},
	)

	return ModelClient{Client: elelem.New(driver), Model: elelem.Model{ID: "m"}}
}

func imageParts(messages []elelem.Message) []elelem.Part {
	var parts []elelem.Part

	for _, message := range messages {
		for _, part := range message.Content {
			if part.Type == elelem.PartTypeImage {
				parts = append(parts, part)
			}
		}
	}

	return parts
}

func TestSavedHistoryKeepsImageReferencesAndResolveRestoresBytes(t *testing.T) {
	t.Parallel()

	first := []byte(testPNGBytes + " first")
	second := []byte(testPNGBytes + " second")
	original := []elelem.Message{
		{Role: elelem.RoleSystem, Content: elelem.Text("system")},
		{
			Role: elelem.RoleUser,
			Content: contentWithImages("look", []session.MessageImage{
				{MediaType: elelem.MediaTypePNG, Data: first},
				{MediaType: elelem.MediaTypePNG, Data: second},
			}),
		},
		{Role: elelem.RoleAssistant, Content: elelem.Text("seen")},
	}

	saved := savedHistory(original)
	require.Len(t, saved, 2)

	references := imageParts(saved)
	require.Len(t, references, 2)

	for _, part := range references {
		assert.Empty(t, part.Image.Data)
		assert.True(t, strings.HasPrefix(part.Image.URL, imageReferencePrefix))
		assert.Equal(t, elelem.MediaTypePNG, part.Image.MediaType)
	}

	assert.Equal(t, first, original[1].Content[1].Image.Data,
		"saving must not change the caller's messages")

	encoded, err := json.Marshal(saved)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), string(session.ImageBlob(first)))

	resolved, err := resolveImageReferences(context.Background(),
		storedBlobs(first, second), saved)
	require.NoError(t, err)

	restored := imageParts(resolved)
	require.Len(t, restored, 2)
	assert.Equal(t, first, restored[0].Image.Data)
	assert.Equal(t, second, restored[1].Image.Data)
	assert.Empty(t, restored[0].Image.URL)
	assert.Equal(t, "look", resolved[0].Text())
	assert.True(t, strings.HasPrefix(saved[0].Content[1].Image.URL,
		imageReferencePrefix), "resolving must not change the saved history")
}

func TestResolveImageReferencesRefusesAMissingBlob(t *testing.T) {
	t.Parallel()

	saved := savedHistory([]elelem.Message{{
		Role: elelem.RoleUser,
		Content: contentWithImages("", []session.MessageImage{
			{MediaType: elelem.MediaTypePNG, Data: []byte(testPNGBytes)},
		}),
	}})

	_, err := resolveImageReferences(context.Background(), mapImageBlobs{},
		saved)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestWorkflowRoundInjectionsShowAReadImageAfterTheRound(t *testing.T) {
	t.Parallel()

	data := []byte(testPNGBytes)
	results := []workflowToolResult{
		{
			Result: elelem.ToolResult{
				Content: "read",
				Metadata: map[string]any{toolImageMetadataKey: toolImage{
					Path: "/work/shot.png", MediaType: elelem.MediaTypePNG,
					Data: data,
				}},
			},
			Injections: []elelem.MessageInjection{
				{Type: elelem.RoleUser, Content: "hook note"},
			},
		},
		{Result: elelem.ToolResult{Content: "plain"}},
	}

	// The round merge reads results after a workflow payload round trip.
	encoded, err := json.Marshal(results)
	require.NoError(t, err)

	var decoded []workflowToolResult
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	messages, err := workflowRoundInjections(decoded)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "hook note", messages[0].Text())

	shown := messages[1]
	assert.Equal(t, elelem.RoleUser, shown.Role)
	assert.Contains(t, shown.Text(), "/work/shot.png")
	assert.Contains(t, shown.Text(), "did not come from the user")

	parts := imageParts(messages)
	require.Len(t, parts, 1)
	assert.Equal(t, imageReferencePrefix+session.ImageBlobHash(data),
		parts[0].Image.URL)
	assert.Empty(t, parts[0].Image.Data)
}

func TestWorkflowRoundInjectionsIgnoreAnErrorResultImage(t *testing.T) {
	t.Parallel()

	messages, err := workflowRoundInjections([]workflowToolResult{{
		Result: elelem.ToolResult{
			IsError: true,
			Metadata: map[string]any{toolImageMetadataKey: toolImage{
				Path: "/work/shot.png", MediaType: elelem.MediaTypePNG,
				Data: []byte(testPNGBytes),
			}},
		},
	}})
	require.NoError(t, err)
	assert.Empty(t, messages)
}

func TestValidateImagesAppliesTheDeploymentLimits(t *testing.T) {
	t.Parallel()

	data := []byte(testPNGBytes)
	image := MessageImage{MediaType: elelem.MediaTypePNG, Data: data}
	runtime := &Runtime{maxImagesPerMessage: 2, maxImageBytes: len(data)}

	testCases := []struct {
		name    string
		images  []MessageImage
		wantErr error
	}{
		{name: "none"},
		{name: "exactly the count limit", images: []MessageImage{image, image}},
		{
			name:    "one over the count limit",
			images:  []MessageImage{image, image, image},
			wantErr: session.ErrTooManyImages,
		},
		{
			name: "one byte over the size limit",
			images: []MessageImage{{
				MediaType: elelem.MediaTypePNG,
				Data:      append([]byte(testPNGBytes), 0),
			}},
			wantErr: session.ErrImageTooLarge,
		},
		{
			name: "bytes that are not the declared type",
			images: []MessageImage{{
				MediaType: elelem.MediaTypeJPEG, Data: data,
			}},
			wantErr: session.ErrImageType,
		},
		{
			name: "a media type no provider takes",
			images: []MessageImage{{
				MediaType: "image/svg+xml", Data: []byte("<svg/>"),
			}},
			wantErr: session.ErrImageType,
		},
		{
			name:    "no bytes",
			images:  []MessageImage{{MediaType: elelem.MediaTypePNG}},
			wantErr: session.ErrImageType,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := runtime.validateImages(tc.images)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestImageInputFollowsTheModelAndTheUpstreamSetting(t *testing.T) {
	t.Parallel()

	images := []MessageImage{{
		MediaType: elelem.MediaTypePNG, Data: []byte(testPNGBytes),
	}}
	withImages := elelem.NewPrompt().User(contentWithImages("look", images)...)
	textOnly := elelem.NewPrompt().UserText("look")
	off := false
	driver := elelemtest.NewScriptedDriver().WithCapabilities(
		elelem.Capabilities{SupportsImageInput: true},
	)
	model := elelem.Model{ID: "m"}
	overridden := ModelClient{
		Client: elelem.New(driver, upstreamClientOptions(driver,
			config.Upstream{ImageInput: &off}, model)...),
		Model: model,
	}

	testCases := []struct {
		name    string
		model   ModelClient
		accepts bool
	}{
		{name: "driver takes images", model: imageModel(t, true), accepts: true},
		{name: "driver takes no images", model: imageModel(t, false)},
		{name: "upstream marked text-only", model: overridden},
		{name: "no client", model: ModelClient{}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.accepts, modelAcceptsImages(tc.model))
			require.NoError(t, requireImageInput(tc.model, nil))
			require.NoError(t, requireHistoryImageInput(tc.model, textOnly))

			if tc.accepts {
				require.NoError(t, requireImageInput(tc.model, images))
				require.NoError(t, requireHistoryImageInput(tc.model,
					withImages))

				return
			}

			err := requireImageInput(tc.model, images)
			require.ErrorIs(t, err, session.ErrImagesNotAccepted)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)

			err = requireHistoryImageInput(tc.model, withImages)
			require.ErrorIs(t, err, session.ErrHistoryImagesNotAccepted)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestImageTokenCounterCountsEachImage(t *testing.T) {
	t.Parallel()

	counter := withImageTokens(nil)
	text := []elelem.Message{{Role: elelem.RoleUser, Content: elelem.Text("look")}}
	images := savedHistory([]elelem.Message{{
		Role: elelem.RoleUser,
		Content: contentWithImages("look", []session.MessageImage{
			{MediaType: elelem.MediaTypePNG, Data: []byte(testPNGBytes)},
			{MediaType: elelem.MediaTypePNG, Data: []byte(testPNGBytes + "2")},
		}),
	}})

	plain, err := counter.Count(text, nil)
	require.NoError(t, err)

	withTwo, err := counter.Count(images, nil)
	require.NoError(t, err)
	assert.Equal(t, plain+2*imageTokenEstimate, withTwo)
}

func TestCompactionTranscriptNotesEachImage(t *testing.T) {
	t.Parallel()

	rendered := renderCompactionMessages(savedHistory([]elelem.Message{{
		Role: elelem.RoleUser,
		Content: contentWithImages("look at this", []session.MessageImage{
			{MediaType: elelem.MediaTypePNG, Data: []byte(testPNGBytes)},
		}),
	}}))

	assert.Contains(t, rendered, "look at this")
	assert.Contains(t, rendered, "[image image/png]")
	assert.NotContains(t, rendered, imageReferencePrefix)
}

func TestReadFileHandlerAttachesAnImageOnlyForAnImageModel(t *testing.T) {
	t.Parallel()

	data := []byte(testPNGBytes)
	handler := readFileHandler(func(
		context.Context,
		tools.ReadFileInput,
	) (tools.ReadFileOutput, error) {
		return tools.ReadFileOutput{
			Path: "/work/shot.png", MediaType: elelem.MediaTypePNG,
			SizeBytes: len(data), Image: data,
		}, nil
	})
	call := elelem.ToolInput{
		Name: toolNameReadFile, CallID: "c1",
		Arguments: json.RawMessage(`{"path":"shot.png"}`),
	}

	attached, err := handler(contextWithImageInput(context.Background(), true),
		call)
	require.NoError(t, err)
	require.False(t, attached.IsError)
	assert.Contains(t, attached.Content, elelem.MediaTypePNG)

	// A committed tool phase compares its saved output with a fresh encoding,
	// so the result must encode the same after a workflow payload round trip.
	first, err := json.Marshal(attached)
	require.NoError(t, err)

	var decoded elelem.ToolResult
	require.NoError(t, json.Unmarshal(first, &decoded))

	second, err := json.Marshal(decoded)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))

	images, err := resultImages(attached)
	require.NoError(t, err)
	require.Len(t, images, 1)
	assert.Equal(t, data, images[0].Data)
	assert.Equal(t, "/work/shot.png", images[0].Path)

	refused, err := handler(context.Background(), call)
	require.NoError(t, err)
	assert.True(t, refused.IsError)
	assert.Contains(t, refused.Content, session.ErrImagesNotAccepted.Error())

	none, err := resultImages(refused)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// A result saved before a result could carry several images keeps its one
// image under the older metadata key, and still shows it.
func TestResultImagesReadsTheSingleImageKey(t *testing.T) {
	t.Parallel()

	saved := elelem.ToolResult{Metadata: map[string]any{
		toolImageMetadataKey: map[string]any{
			"path": "/work/shot.png", "mediaType": elelem.MediaTypePNG,
			"data": []byte(testPNGBytes),
		},
	}}

	images, err := resultImages(saved)
	require.NoError(t, err)
	require.Len(t, images, 1)
	assert.Equal(t, "/work/shot.png", images[0].Path)
	assert.Equal(t, []byte(testPNGBytes), images[0].Data)

	malformed := elelem.ToolResult{Metadata: map[string]any{
		toolImagesMetadataKey: "not a list",
	}}
	_, err = resultImages(malformed)
	require.Error(t, err)
}
