//go:build real

package realtest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	realImageSide      = 64
	realImageMediaType = "image/png"
	realImageFile      = "swatch.png"

	realImageMessageTask = `What single color fills the attached image? ` +
		`Answer with the color name only.`
	realImageReadFileTask = `Call readFile on ` + realImageFile + ` in the ` +
		`current directory, look at the image it returns, and answer with ` +
		`the name of the single color that fills it. Do not run commands.`
)

type realImageWebSocketMessage struct {
	Message string            `json:"message"`
	Images  []realImageSource `json:"images"`
}

type realImageSource struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
}

func realSolidPNG(t *testing.T, fill color.RGBA) []byte {
	t.Helper()

	picture := image.NewRGBA(image.Rect(0, 0, realImageSide, realImageSide))
	for x := range realImageSide {
		for y := range realImageSide {
			picture.Set(x, y, fill)
		}
	}

	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, picture))

	return encoded.Bytes()
}

func lastRealAssistantText(messages []api.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == api.MessageRoleAssistant &&
			strings.TrimSpace(messages[index].Content) != "" {
			return messages[index].Content
		}
	}

	return ""
}

// TestRealHarnessShowsTheModelImages proves an image reaches a real
// image-capable model both ways Peen accepts one: attached to message.send,
// and read from the workspace with readFile. Each image is one solid color the
// model can only name by seeing it.
func TestRealHarnessShowsTheModelImages(t *testing.T) {
	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	encodedUpstreams, err := json.Marshal(upstreams)
	require.NoError(t, err)

	fixture := newRealHarnessFixture(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(fixture.service, realImageFile),
		realSolidPNG(t, color.RGBA{B: 255, A: 255}),
		0o600,
	))

	binary := buildRealHarnessBinary(t)
	process := startRealHarnessPeen(
		t,
		binary,
		fixture,
		string(encodedUpstreams),
		configured.DefaultModel,
	)
	t.Cleanup(func() { process.stop(t) })

	sessionID := openRealHarnessSession(t, process, fixture.service)
	connection := dialRealHarnessWebSocket(t, process)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	sent := writeRealImageMessage(t, connection, sessionID,
		realSolidPNG(t, color.RGBA{R: 255, A: 255}))
	awaitRealHarnessWebSocketCompletion(t, process, connection, sent)

	messages := listRealHarnessMessages(t, process.baseURL, process.apiToken,
		sessionID)
	require.NotEmpty(t, messages)
	require.NotNil(t, messages[0].Images)
	assert.Len(t, *messages[0].Images, 1)
	assert.Contains(t, strings.ToLower(lastRealAssistantText(messages)), "red")

	sent = writeRealHarnessMessage(t, connection, sessionID,
		realImageReadFileTask)
	awaitRealHarnessWebSocketCompletion(t, process, connection, sent)

	messages = listRealHarnessMessages(t, process.baseURL, process.apiToken,
		sessionID)
	assert.Contains(t, realHarnessToolCallNames(messages), "readFile")
	assert.Contains(t, strings.ToLower(lastRealAssistantText(messages)), "blue")

	var shown bool
	for _, message := range messages {
		if message.InjectedKind != nil &&
			*message.InjectedKind == api.MessageInjectedKindImage {
			shown = true
		}
	}

	assert.True(t, shown, "readFile stored the image it showed the model")
}

func writeRealImageMessage(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	data []byte,
) uuid.UUID {
	t.Helper()

	event := dabluveees.NewEvent(
		realHarnessMessageSendEvent,
		realImageWebSocketMessage{
			Message: realImageMessageTask,
			Images: []realImageSource{{
				MediaType: realImageMediaType,
				Data:      base64.StdEncoding.EncodeToString(data),
			}},
		},
	).SetMetadata(realHarnessWebSocketSessionID, sessionID.String())
	require.NoError(t, connection.WriteJSON(event))

	return event.ID
}
