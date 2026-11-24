package telegram

import (
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenValidation(t *testing.T) {
	t.Parallel()
	for _, token := range []string{
		"", "EXAMPLE-DO-NOT-USE", ":EXAMPLE",
		"0:EXAMPLE", "+1:EXAMPLE", "1:", "1:bad/path", "1:bad?query",
		"1:bad\nsecret", "1:bad:secret",
		strings.Repeat("1", maxTokenBytes+1),
	} {
		_, err := New(token)
		require.ErrorIs(t, err, commerr.ErrValidationFailed)
		if token != "" {
			assert.NotContains(t, err.Error(), token)
		}
	}
	client, err := New(testToken)
	require.NoError(t, err)
	assert.Equal(t, apiBaseURL, client.baseURL)
	assert.NotNil(t, client.http.CheckRedirect)
	assert.Equal(t, clientTimeout, client.http.Timeout)
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter, _ *http.Request,
	) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	client := testClient(t, func(
		w http.ResponseWriter, r *http.Request,
	) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			writeResult(t, w, File{
				FileID: "document", FilePath: "documents/file",
			})

			return
		}
		w.Header().Set("Location", target.URL+"/"+testToken)
		w.WriteHeader(http.StatusFound)
	})
	_, err := client.SendMessage(t.Context(), 12, "hello")
	require.ErrorIs(t, err, commerr.ErrUnavailable)
	assertSafeError(t, err)
	_, err = client.Download(t.Context(), "document", 10)
	require.ErrorIs(t, err, commerr.ErrUnavailable)
	assertSafeError(t, err)
	assert.Zero(t, requests.Load())
}

func TestInputBoundsDoNotSend(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	client := testClient(t, func(
		w http.ResponseWriter, _ *http.Request,
	) {
		requests.Add(1)
		_, err := io.WriteString(w, `{"ok":false}`)
		require.NoError(t, err)
	})
	_, err := client.GetUpdates(t.Context(), -2, 0)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = client.GetUpdates(t.Context(), 0, 51)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = client.SendMessage(t.Context(), 0, "hello")
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = client.SendMessage(t.Context(), 12, "")
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = client.SendMessage(t.Context(), 12, strings.Repeat("x", 4097))
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = client.SendDocument(t.Context(), 12, "bad\nname", []byte("a"))
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = client.SendPhoto(t.Context(), 12, "file", nil)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = client.SendPhoto(t.Context(), 12, "file",
		make([]byte, maxPhotoBytes+1))
	require.ErrorIs(t, err, commerr.ErrOutOfRange)
	_, err = client.SendDocument(t.Context(), 12, "file",
		make([]byte, maxUploadBytes+1))
	require.ErrorIs(t, err, commerr.ErrOutOfRange)
	_, err = client.Download(t.Context(), "document", maxDownloadBytes+1)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Zero(t, requests.Load())
}

func TestInvalidUpdateIDsAreRefused(t *testing.T) {
	t.Parallel()
	for _, id := range []int64{0, -1, math.MaxInt64} {
		t.Run(strconv.FormatInt(id, 10), func(t *testing.T) {
			t.Parallel()
			client := testClient(t, func(
				w http.ResponseWriter, _ *http.Request,
			) {
				writeResult(t, w, []Update{{ID: id}})
			})
			_, err := client.GetUpdates(t.Context(), 0, 0)
			require.ErrorIs(t, err, commerr.ErrInvalidState)
			assertSafeError(t, err)
		})
	}
}
