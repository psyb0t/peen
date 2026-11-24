package telegram

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

const testToken = "1:EXAMPLE-DO-NOT-USE"

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := newClient(testToken, server.URL, server.Client())
	require.NoError(t, err)

	return client
}

func writeResult(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "result": value,
	}))
}

func sentMessage(chatID int64) Message {
	return Message{ID: 7, Chat: Chat{ID: chatID}}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// Read only the bounded wire parts; no temporary multipart files are created.
func readMultipartUpload(
	t *testing.T, w http.ResponseWriter, r *http.Request, field, chatID string,
) (string, []byte) {
	t.Helper()
	r.Body = http.MaxBytesReader(w, r.Body, 4096)

	reader, err := r.MultipartReader()
	require.NoError(t, err)

	var name string
	var data []byte

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}

		require.NoError(t, err)
		value, err := io.ReadAll(part)
		require.NoError(t, err)
		require.NoError(t, part.Close())

		switch part.FormName() {
		case fieldChatID:
			require.Equal(t, chatID, string(value))
		case field:
			name, data = part.FileName(), value
		default:
			t.Fatalf("unexpected multipart field %q", part.FormName())
		}
	}

	require.NotEmpty(t, name)

	return name, data
}
