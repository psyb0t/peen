package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cutStreamModelID = "cut-stream-model"

	// cutStreamKeepAlive is an SSE comment: it opens the stream and carries
	// no chunk.
	cutStreamKeepAlive = ": keep-alive\n\n"
)

func cutStreamChunk(delta, finishReason string) string {
	reason := "null"
	if finishReason != "" {
		reason = fmt.Sprintf("%q", finishReason)
	}

	return fmt.Sprintf(
		`data: {"id":"chunk","object":"chat.completion.chunk",`+
			`"model":%q,"choices":[{"index":0,`+
			`"delta":{"content":%q},"finish_reason":%s}]}`+"\n\n",
		cutStreamModelID, delta, reason,
	)
}

// cutStreamServer answers every request with the next body of bodies, the
// last one repeating, then closes the connection cleanly.
func cutStreamServer(t *testing.T, bodies ...string) (string, *atomic.Int32) {
	t.Helper()

	var requests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		index := min(int(requests.Add(1))-1, len(bodies)-1)

		writer.Header().Set("Content-Type", "text/event-stream")

		_, err := io.WriteString(writer, bodies[index])
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	return server.URL, &requests
}

//nolint:ireturn // NewDriver returns the elelem.Driver contract.
func cutStreamDriver(t *testing.T, baseURL string) elelem.Driver {
	t.Helper()

	driver, err := NewDriver(config.Upstream{
		Name:    "cut",
		Type:    config.UpstreamTypeOpenAI,
		BaseURL: baseURL,
	})
	require.NoError(t, err)

	return driver
}

func cutStreamRequest() elelem.DriverRequest {
	return elelem.DriverRequest{
		Model: elelem.Model{ID: cutStreamModelID},
		Messages: []elelem.Message{{
			Role: elelem.RoleUser, Content: elelem.Text("hello"),
		}},
	}
}

// A stream closed cleanly before its first delta is retried, and the next
// attempt that finishes is the answer.
func TestProviderStreamCutBeforeFirstDeltaIsRetried(t *testing.T) {
	t.Parallel()

	baseURL, requests := cutStreamServer(t,
		cutStreamKeepAlive,
		cutStreamChunk("whole answer", "stop")+"data: [DONE]\n\n",
	)

	var text string

	usage, err := cutStreamDriver(t, baseURL).Stream(t.Context(),
		cutStreamRequest(), func(delta elelem.Delta) error {
			text += delta.Text

			return nil
		})
	require.NoError(t, err)
	assert.Equal(t, "whole answer", text)
	assert.Equal(t, elelem.FinishReasonStop, usage.FinishReason)
	assert.Equal(t, 2, usage.Retry.TotalAttempts)
	assert.EqualValues(t, 2, requests.Load())
}

// A stream closed cleanly after output reached the caller fails the call
// instead of passing the partial text off as the answer, and is not replayed.
func TestProviderStreamCutAfterADeltaFails(t *testing.T) {
	t.Parallel()

	baseURL, requests := cutStreamServer(t,
		cutStreamChunk("partial", ""),
	)

	var text string

	_, err := cutStreamDriver(t, baseURL).Stream(t.Context(),
		cutStreamRequest(), func(delta elelem.Delta) error {
			text += delta.Text

			return nil
		})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Equal(t, "partial", text)
	assert.EqualValues(t, 1, requests.Load())
}
