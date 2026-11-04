//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"path"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/aichteeteapee"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	imageTestMediaType = "image/png"
	imageTestSide      = 8
	// imageTestDataURLPrefix is how the OpenAI-compatible driver sends an
	// image's bytes to the provider.
	imageTestDataURLPrefix = "data:image/png;base64,"
)

// logAppOnImageFailure prints the controller's logs when the test fails,
// because an internal failure answers the client with no detail.
func logAppOnImageFailure(t *testing.T) {
	t.Helper()

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		logs, err := integrationInfra.AppLogs(ctx)
		require.NoError(t, err)
		t.Logf("image test container logs: %s", logs)
	})
}

func imageTestPNG(t *testing.T) []byte {
	t.Helper()

	picture := image.NewRGBA(image.Rect(0, 0, imageTestSide, imageTestSide))
	picture.Set(2, 3, color.RGBA{G: 200, A: 255})

	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, picture))

	return encoded.Bytes()
}

func sendImageMessage(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	message string,
	images []map[string]string,
) {
	t.Helper()

	event := dabluveees.NewEvent(
		apiTestWebSocketMessageSend,
		map[string]any{"message": message, "images": images},
	).SetMetadata(
		apiTestWebSocketSessionIDParameter,
		sessionID.String(),
	)

	assertValidMessageSend(t, event)
	require.NoError(t, connection.WriteJSON(event))
}

// providerImageMessages returns the user messages of a provider request that
// carry an image of data.
func providerImageMessages(
	request []testinfra.ProviderMessage,
	data []byte,
) []testinfra.ProviderMessage {
	encoded := imageTestDataURLPrefix + base64.StdEncoding.EncodeToString(data)
	matching := make([]testinfra.ProviderMessage, 0)

	for _, message := range request {
		if message.Role == "user" && strings.Contains(message.Content, encoded) {
			matching = append(matching, message)
		}
	}

	return matching
}

func TestMessageImagesReachTheModelAndReplayOnTheNextTurn(t *testing.T) {
	logAppOnImageFailure(t)

	workspace := openFreshWorkspace(t, "message-images-", nil)
	connection := openSessionConnection(t, workspace.sessionID)
	data := imageTestPNG(t)

	before := len(integrationInfra.ProviderRequests())
	sendImageMessage(t, connection, workspace.sessionID, "what is in this",
		[]map[string]string{{
			"mediaType": imageTestMediaType,
			"data":      base64.StdEncoding.EncodeToString(data),
		}})

	frames := readFramesUntilTurnCompleted(t, connection)
	created := framesOfType(frames, "userMessage.created")
	require.Len(t, created, 1)
	assert.Contains(t, string(created[0].Data), `"sizeBytes"`)
	assert.NotContains(t, string(created[0].Data),
		base64.StdEncoding.EncodeToString(data))

	requests := providerRequestsSince(before)
	require.NotEmpty(t, requests)
	shown := providerImageMessages(requests[0], data)
	require.Len(t, shown, 1)
	assert.Contains(t, shown[0].Content, "what is in this")

	stored := storedMessages(t, workspace.sessionID)
	require.NotEmpty(t, stored)
	require.NotNil(t, stored[0].Images)
	assert.Equal(t, []api.MessageImageInfo{{
		MediaType: imageTestMediaType, SizeBytes: int64(len(data)),
	}}, *stored[0].Images)

	before = len(integrationInfra.ProviderRequests())
	sendSessionMessage(t, connection, workspace.sessionID, "and now?")
	readFramesUntilTurnCompleted(t, connection)

	requests = providerRequestsSince(before)
	require.NotEmpty(t, requests)
	assert.Len(t, providerImageMessages(requests[0], data), 1,
		"the next turn replays the stored image")
}

func TestReadFileShowsTheModelAWorkspaceImage(t *testing.T) {
	logAppOnImageFailure(t)

	data := imageTestPNG(t)
	workspace := openFreshWorkspace(t, "readfile-image-", map[string]string{
		"shot.png": string(data),
	})
	imagePath := path.Join(workspace.directory, "shot.png")
	const task = "look at the screenshot"

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: task, FinalAnswer: "seen",
		Calls: []testinfra.ScriptedToolCall{
			{Name: "readFile", Arguments: map[string]any{"path": imagePath}},
		},
	})

	connection := openSessionConnection(t, workspace.sessionID)
	before := len(integrationInfra.ProviderRequests())
	sendSessionMessage(t, connection, workspace.sessionID, task)
	readFramesUntilTurnCompleted(t, connection)

	requests := providerRequestsSince(before)
	require.GreaterOrEqual(t, len(requests), 2)

	last := requests[len(requests)-1]
	shown := providerImageMessages(last, data)
	require.Len(t, shown, 1)
	assert.Contains(t, shown[0].Content, imagePath)
	assert.Contains(t, shown[0].Content, "did not come from the user")

	toolIndex, imageIndex := -1, -1
	for index, message := range last {
		if message.Role == "tool" && toolIndex < 0 {
			toolIndex = index
		}

		if message.Content == shown[0].Content {
			imageIndex = index
		}
	}

	require.GreaterOrEqual(t, toolIndex, 0)
	assert.Greater(t, imageIndex, toolIndex,
		"the image follows the tool result")

	var injected *api.Message
	for _, message := range storedMessages(t, workspace.sessionID) {
		if message.InjectedKind != nil &&
			*message.InjectedKind == api.MessageInjectedKindImage {
			injected = &message
		}
	}

	require.NotNil(t, injected)
	require.NotNil(t, injected.Images)
	assert.Equal(t, []api.MessageImageInfo{{
		MediaType: imageTestMediaType, SizeBytes: int64(len(data)),
	}}, *injected.Images)
}

func TestMessageImagesThatAreNotTheirTypeAreRefused(t *testing.T) {
	workspace := openFreshWorkspace(t, "message-images-refused-", nil)
	connection := openSessionConnection(t, workspace.sessionID)

	before := len(integrationInfra.ProviderRequests())
	sendImageMessage(t, connection, workspace.sessionID, "look",
		[]map[string]string{{
			"mediaType": imageTestMediaType,
			"data":      base64.StdEncoding.EncodeToString([]byte("plain text")),
		}})

	failure := awaitAPIWebSocketFailure(t, connection)
	assert.Equal(t, aichteeteapee.ErrorCodeValidationFailed, failure.Code)
	assert.Contains(t, failure.Message, "not PNG, JPEG, GIF, or WebP data")
	assert.Len(t, integrationInfra.ProviderRequests(), before)
	assert.Empty(t, storedMessages(t, workspace.sessionID))
}
