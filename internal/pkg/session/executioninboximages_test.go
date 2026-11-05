package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const inboxImageMediaType = "image/png"

func inboxTestImages() []MessageImage {
	return []MessageImage{
		{MediaType: inboxImageMediaType, Data: []byte("first image bytes")},
		{MediaType: inboxImageMediaType, Data: []byte("second image")},
	}
}

func TestAcceptExecutionMessageStoresItsImages(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	input.Images = inboxTestImages()

	accepted, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)

	stored, err := DecodeStoredImages(accepted.Message.ImagesJSON)
	require.NoError(t, err)
	require.Len(t, stored, len(input.Images))

	for index, image := range input.Images {
		assert.Equal(t, image.MediaType, stored[index].MediaType)
		assert.Equal(t, len(image.Data), stored[index].SizeBytes)
		assert.Equal(t, ImageBlobHash(image.Data), stored[index].Blob)
	}

	blobs, err := fixture.store.GetContentBlobs(t.Context(),
		[]string{stored[0].Blob, stored[1].Blob})
	require.NoError(t, err)
	data, err := DecodeImageBlob(blobs[stored[0].Blob])
	require.NoError(t, err)
	assert.Equal(t, input.Images[0].Data, data)

	require.Len(t, accepted.Events, 2)

	var payload executionInboxMessagePayload
	require.NoError(t, json.Unmarshal(
		[]byte(accepted.Events[0].PayloadJSON), &payload))
	assert.Equal(t, []ImageDescription{
		{MediaType: inboxImageMediaType, SizeBytes: len(input.Images[0].Data)},
		{MediaType: inboxImageMediaType, SizeBytes: len(input.Images[1].Data)},
	}, payload.Images)

	repeated, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	assert.Equal(t, accepted.Message.ID, repeated.Message.ID)
}

func TestAcceptExecutionMessageRefusesChangedImages(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		change func(*AcceptExecutionMessageInput)
	}{
		{name: "image removed", change: func(input *AcceptExecutionMessageInput) {
			input.Images = input.Images[:1]
		}},
		{name: "image changed", change: func(input *AcceptExecutionMessageInput) {
			input.Images[1].Data = []byte("other bytes")
		}},
		{name: "images dropped", change: func(input *AcceptExecutionMessageInput) {
			input.Images = nil
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			lease := inboxTestLease(t, fixture)
			input := inboxTestInput()
			input.Images = inboxTestImages()
			_, err := fixture.store.AcceptExecutionMessage(t.Context(),
				fixture.root.ID, lease, input)
			require.NoError(t, err)

			tc.change(&input)
			_, err = fixture.store.AcceptExecutionMessage(t.Context(),
				fixture.root.ID, lease, input)
			require.ErrorIs(t, err, commerr.ErrConflict)
		})
	}
}

func TestAcceptExecutionMessageRefusesAnImageWithoutData(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	input.Images = []MessageImage{{MediaType: inboxImageMediaType}}

	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestDeliverExecutionMessagesKeepsTheMessageImages(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	input.Images = inboxTestImages()
	accepted, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)

	requestIDs := []uuid.UUID{input.RequestID}
	claim := inboxTestClaim(t, fixture, requestIDs)
	delivered, err := fixture.store.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, DeliverExecutionMessagesInput{
			TurnLease: lease, ModelLease: claim.Lease, RequestIDs: requestIDs,
		})
	require.NoError(t, err)

	var payload executionInboxMessagePayload
	require.NoError(t, json.Unmarshal(
		[]byte(delivered.Events[0].PayloadJSON), &payload))
	assert.Len(t, payload.Images, len(input.Images))

	messages, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, messages.Items, 3)
	assert.Equal(t, input.Message, messages.Items[1].Content)
	assert.Equal(t, accepted.Message.ImagesJSON, messages.Items[1].ImagesJSON)
	assert.Equal(t, emptyImagesJSON, messages.Items[2].ImagesJSON,
		"the skill message after it carries no images")
}

func TestAcceptExecutionMessageWithoutImagesStoresAnEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)

	accepted, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, inboxTestInput())
	require.NoError(t, err)
	assert.Equal(t, emptyImagesJSON, accepted.Message.ImagesJSON)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(
		[]byte(accepted.Events[0].PayloadJSON), &payload))
	assert.NotContains(t, payload, "images")
}
