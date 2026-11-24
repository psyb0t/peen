package telegram

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownload(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := testClient(t, func(
		w http.ResponseWriter, r *http.Request,
	) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			var request fileRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, "document", request.FileID)
			writeResult(t, w, File{
				FileID: "document", FileSize: 3,
				FilePath: "documents/file_1.txt",
			})

			return
		}
		assert.Equal(t, "/file/bot"+testToken+"/documents/file_1.txt",
			r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		_, err := w.Write([]byte("hey"))
		require.NoError(t, err)
	})
	data, err := client.Download(t.Context(), "document", 3)
	require.NoError(t, err)
	assert.Equal(t, []byte("hey"), data)
	assert.EqualValues(t, 2, calls.Load())
}

func TestDownloadRefusesUntrustedPaths(t *testing.T) {
	t.Parallel()
	testCases := []string{
		"/absolute", "../escape", "a/../b", "a/./b",
		"a//b", "a/", "a%2fb", "https://example.invalid/file",
		"//example.invalid/file", "a?query", "a#fragment", "a\\b",
		"a\nb", "a/..", "%252e%252e/secret",
	}
	for _, path := range testCases {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			client := testClient(t, func(
				w http.ResponseWriter, _ *http.Request,
			) {
				calls.Add(1)
				writeResult(t, w, File{FileID: "document", FilePath: path})
			})
			_, err := client.Download(t.Context(), "document", 10)
			require.ErrorIs(t, err, commerr.ErrInvalidState)
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestDownloadBounds(t *testing.T) {
	t.Parallel()
	for _, declared := range []int64{0, 4} {
		t.Run(strconv.FormatInt(declared, 10), func(t *testing.T) {
			t.Parallel()
			var downloads atomic.Int32
			client := testClient(t, func(
				w http.ResponseWriter, r *http.Request,
			) {
				if strings.HasSuffix(r.URL.Path, "/getFile") {
					writeResult(t, w, File{
						FileID: "document", FileSize: declared,
						FilePath: "documents/file.txt",
					})

					return
				}
				downloads.Add(1)
				_, err := w.Write([]byte("long"))
				require.NoError(t, err)
			})
			_, err := client.Download(t.Context(), "document", 3)
			require.ErrorIs(t, err, commerr.ErrOutOfRange)
			if declared == 4 {
				assert.Zero(t, downloads.Load())
			}
		})
	}
}
