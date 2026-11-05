package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func queuedTestImage() MessageImage {
	return MessageImage{
		MediaType: elelem.MediaTypePNG, Data: []byte(testPNGBytes),
	}
}

func imageCapableDriver(acceptsImages bool) *elelemtest.ScriptedDriver {
	return elelemtest.NewScriptedDriver().WithCapabilities(
		elelem.Capabilities{SupportsImageInput: acceptsImages},
	)
}

// A message queued into a running turn keeps its images in the inbox, and
// the next round shows them to the model after the message text.
func TestQueuedMessageCarriesItsImagesToTheNextRound(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		imageCapableDriver(true))
	image := queuedTestImage()

	result, handled, err := fixture.base.runtime.queueActiveUserMessage(
		t.Context(), TurnRequest{
			RequestID: uuid.New(), SessionID: &fixture.input.SessionID,
			Message: "look at this", Images: []MessageImage{image},
		})
	require.NoError(t, err)
	require.True(t, handled)
	assert.True(t, result.Queued)

	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{
			State: models.InboxMessageStateQueued,
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)

	var conversation workflowConversation

	require.NoError(t, appendRootInboxContext(&conversation,
		prepared.skillLedger, page.Items[0]))
	require.Len(t, conversation.Messages, 1)

	content := conversation.Messages[0].Content
	require.Len(t, content, 2)
	assert.Equal(t, "look at this", content[0].Text)
	require.NotNil(t, content[1].Image)
	assert.Equal(t, imageReferencePrefix+session.ImageBlobHash(image.Data),
		content[1].Image.URL)
	assert.Empty(t, content[1].Image.Data,
		"the saved context names the blob instead of carrying bytes")

	loaded, err := loadStoredImages(t.Context(), fixture.base.store,
		page.Items[0].ImagesJSON)
	require.NoError(t, err)
	assert.Equal(t, []MessageImage{image}, loaded)
}

func TestQueuedImagesNeedAModelThatTakesImages(t *testing.T) {
	t.Parallel()
	fixture, _ := newRootModelWorkflowFixture(t, imageCapableDriver(false))

	_, handled, err := fixture.base.runtime.queueActiveUserMessage(
		t.Context(), TurnRequest{
			RequestID: uuid.New(), SessionID: &fixture.input.SessionID,
			Message: "look at this", Images: []MessageImage{queuedTestImage()},
		})
	require.ErrorIs(t, err, session.ErrImagesNotAccepted)
	assert.True(t, handled)

	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items, "a refused message leaves no inbox row")
}

// A message to a child gets the same image checks as one that starts a
// turn, before Peen looks for the child.
func TestChildMessageImagesAreChecked(t *testing.T) {
	t.Parallel()
	fixture, _ := newRootModelWorkflowFixture(t, imageCapableDriver(true))
	runtime := fixture.base.runtime
	child := uuid.New()

	tooMany := make([]MessageImage, runtime.maxImagesPerMessage+1)
	for index := range tooMany {
		tooMany[index] = queuedTestImage()
	}

	testCases := []struct {
		name    string
		images  []MessageImage
		wantErr error
	}{
		{name: "too many", images: tooMany, wantErr: session.ErrTooManyImages},
		{
			name: "not its declared type",
			images: []MessageImage{{
				MediaType: elelem.MediaTypePNG, Data: []byte("not a png"),
			}},
			wantErr: session.ErrImageType,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := runtime.RunMessage(t.Context(), MessageRequest{
				SessionID: &fixture.input.SessionID, To: &child,
				Message: "look at this", Images: tc.images,
			}, uuid.New(), nil)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestLoadStoredImagesOfAMessageWithoutImages(t *testing.T) {
	t.Parallel()
	fixture, _ := newRootModelWorkflowFixture(t, imageCapableDriver(true))

	for _, imagesJSON := range []string{"", "[]"} {
		images, err := loadStoredImages(t.Context(), fixture.base.store,
			imagesJSON)
		require.NoError(t, err)
		assert.Empty(t, images)
	}

	_, err := loadStoredImages(t.Context(), fixture.base.store,
		`[{"mediaType":"image/png","sizeBytes":3,"blob":"missing"}]`)
	require.Error(t, err)
}

func TestStoredReferenceContentKeepsPlainTextAsOnePart(t *testing.T) {
	t.Parallel()

	content, err := storedReferenceContent("just text", "[]")
	require.NoError(t, err)
	assert.Equal(t, elelem.Text("just text"), content)

	_, err = storedReferenceContent("text", "not json")
	require.Error(t, err)
}
