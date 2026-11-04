package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testImageMediaType = "image/png"
	// testLongStringBytes is over the 512-byte threshold content blob
	// compaction moves out of a stored document.
	testLongStringBytes = 4096
)

func storedImageBytes(
	ctx context.Context,
	t *testing.T,
	store *Store,
	image StoredImage,
) []byte {
	t.Helper()

	found, err := store.GetContentBlobs(ctx, []string{image.Blob})
	require.NoError(t, err)
	require.Contains(t, found, image.Blob)

	data, err := DecodeImageBlob(found[image.Blob])
	require.NoError(t, err)

	return data
}

func TestMessageImagesRoundTripThroughContentBlobs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	stored := newTestSession(ctx, t, store)
	shared := []byte("\x89PNG\r\n\x1a\nshared image bytes")
	other := []byte("\x89PNG\r\n\x1a\nsecond image bytes")

	lease, err := store.AcquireTurn(ctx, stored.ID, StartTurnInput{
		RequestID: uuid.New(),
		Workspace: testWorkspace,
	})
	require.NoError(t, err)
	require.NoError(t, store.FinalizeTurn(ctx, lease, FinalizeTurnInput{
		State: models.TurnStateCompleted,
		Messages: []MessageInput{
			{
				Role: models.MessageRoleUser, Content: "two images",
				Images: []MessageImage{
					{MediaType: testImageMediaType, Data: shared},
					{MediaType: testImageMediaType, Data: other},
				},
			},
			{Role: models.MessageRoleAssistant, Content: "no images"},
			{
				Role: models.MessageRoleUser, Content: "the first again",
				Injected: true, InjectedKind: models.InjectedKindImage,
				Images: []MessageImage{
					{MediaType: testImageMediaType, Data: shared},
				},
			},
		},
	}))

	history, err := store.CompletedHistory(ctx, stored.ID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 3)

	first, err := DecodeStoredImages(history.Messages[0].ImagesJSON)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, testImageMediaType, first[0].MediaType)
	assert.Equal(t, len(shared), first[0].SizeBytes)
	assert.Equal(t, shared, storedImageBytes(ctx, t, store, first[0]))
	assert.Equal(t, other, storedImageBytes(ctx, t, store, first[1]))

	none, err := DecodeStoredImages(history.Messages[1].ImagesJSON)
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.Equal(t, emptyImagesJSON, history.Messages[1].ImagesJSON)

	again, err := DecodeStoredImages(history.Messages[2].ImagesJSON)
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.Equal(t, first[0].Blob, again[0].Blob)
	assert.Equal(t, ImageBlobHash(shared), again[0].Blob)
	assert.Equal(t, models.InjectedKindImage, history.Messages[2].InjectedKind)
}

func TestMessageImageWithoutDataIsRefused(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		image MessageImage
	}{
		{name: "no bytes", image: MessageImage{MediaType: testImageMediaType}},
		{name: "no media type", image: MessageImage{Data: []byte("x")}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store, handle := openTestStore(t)
			t.Cleanup(func() { require.NoError(t, handle.Close()) })

			stored := newTestSession(ctx, t, store)
			lease, err := store.AcquireTurn(ctx, stored.ID, StartTurnInput{
				RequestID: uuid.New(),
				Workspace: testWorkspace,
			})
			require.NoError(t, err)

			err = store.AppendCheckpoint(ctx, lease, []MessageInput{{
				Role: models.MessageRoleUser, Content: "bad image",
				Images: []MessageImage{tc.image},
			}}, nil)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)

			require.NoError(t, store.FinalizeTurn(ctx, lease,
				FinalizeTurnInput{State: models.TurnStateCompleted}))

			page, err := store.ListMessages(ctx, stored.ID,
				ListMessagesOptions{Limit: 10})
			require.NoError(t, err)

			for _, message := range page.Items {
				assert.NotEqual(t, "bad image", message.Content)
			}
		})
	}
}

func TestModelCallRequestKeepsLongStringsInContentBlobs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	owner := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, owner.ID)
	run := createModelRunForTest(ctx, t, store, owner.ID, lease.TurnID, 0)

	long := strings.Repeat("image bytes as base64 ", testLongStringBytes/22)
	request, err := json.Marshal([]map[string]any{
		{"role": "user", "content": long},
		{"role": "user", "content": long},
	})
	require.NoError(t, err)

	created, err := store.CreateModelCall(ctx, owner.ID, run.ID,
		CreateModelCallInput{
			ID:                  uuid.New(),
			RequestMessagesJSON: string(request),
			RequestToolsJSON:    modelRunTestToolsJSON,
		})
	require.NoError(t, err)
	assert.JSONEq(t, string(request), created.RequestMessagesJSON)

	page, err := store.ListModelCalls(ctx, owner.ID, run.ID,
		ListModelCallsOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.JSONEq(t, string(request), page.Items[0].RequestMessagesJSON)

	literal, err := json.Marshal(long)
	require.NoError(t, err)

	hash := contentblob.Hash(literal)
	found, err := store.GetContentBlobs(ctx, []string{hash})
	require.NoError(t, err)
	assert.Contains(t, found, hash)
}
