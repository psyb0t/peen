package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
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
func TestProviderStreamPublishesBeforeCompletion(t *testing.T) {
	t.Parallel()

	delivered := make(chan struct{})
	release := make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter, request *http.Request,
	) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, err := io.WriteString(writer, cutStreamChunk("live", ""))
		assert.NoError(t, err)
		flusher, ok := writer.(http.Flusher)
		require.True(t, ok)
		flusher.Flush()
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		_, err = io.WriteString(writer,
			cutStreamChunk(" answer", "stop")+"data: [DONE]\n\n")
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(finish)
	driver := cutStreamDriver(t, server.URL)
	done := make(chan error, 1)
	go func() {
		_, err := driver.Stream(t.Context(), cutStreamRequest(),
			func(delta elelem.Delta) error {
				if delta.Text == "live" {
					close(delivered)
				}

				return nil
			})
		done <- err
	}()
	select {
	case <-delivered:
	case <-time.After(workflowTestResultTimeout):
		t.Error("delta was withheld until provider completion")
	}
	finish()
	require.NoError(t, <-done)
}

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

func TestProviderStreamCutAfterADeltaStaysLive(t *testing.T) {
	t.Parallel()

	baseURL, requests := cutStreamServer(t,
		cutStreamChunk("partial", ""),
		cutStreamChunk("whole answer", "stop")+"data: [DONE]\n\n",
	)

	var text string

	usage, err := cutStreamDriver(t, baseURL).Stream(t.Context(),
		cutStreamRequest(), func(delta elelem.Delta) error {
			text += delta.Text

			return nil
		})
	require.ErrorIs(t, err, errProviderStreamCut)
	assert.Equal(t, "partial", text)
	assert.Equal(t, 1, usage.Retry.TotalAttempts)
	assert.EqualValues(t, 1, requests.Load())
}

func TestProviderStreamTurnKeepsOnlyCompletedAttempt(t *testing.T) {
	t.Parallel()

	baseURL, requests := cutStreamServer(t,
		cutStreamChunk("partial", ""),
		cutStreamChunk("whole answer", "stop")+"data: [DONE]\n\n")
	base := newNativeRunnerFixture(t, cutStreamDriver(t, baseURL))
	result, err := base.runtime.Run(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "answer the request",
	})
	require.NoError(t, err)
	assert.Equal(t, "whole answer", result.Text)
	assert.EqualValues(t, 2, requests.Load())
	messages, err := base.store.ListMessages(t.Context(),
		base.runtime.sessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	var answers []string
	for _, message := range messages.Items {
		if message.Role == models.MessageRoleAssistant {
			answers = append(answers, message.Content)
			assert.False(t, message.Incomplete)
		}
	}
	assert.Equal(t, []string{"whole answer"}, answers)
	runs, err := base.store.ListModelRuns(t.Context(), base.runtime.sessionID,
		session.ListModelRunsOptions{})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	calls, err := base.store.ListModelCalls(t.Context(),
		base.runtime.sessionID, runs.Items[0].ID,
		session.ListModelCallsOptions{})
	require.NoError(t, err)
	require.Len(t, calls.Items, 1)
	assert.EqualValues(t, 2, calls.Items[0].TotalAttempts)
	assert.EqualValues(t, 1, calls.Items[0].RetryAttemptCount)
	assert.Contains(t, calls.Items[0].RetryAttemptsJSON,
		"without a finish reason")
	events, err := base.store.ListEvents(t.Context(), base.runtime.sessionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	var starts, stops []int
	var retries int
	for _, event := range events.Items {
		switch event.EventType {
		case essessey.EventTypeContentBlockStart,
			essessey.EventTypeContentBlockStop:
			var block struct {
				Index int `json:"index"`
			}
			require.NoError(t, json.Unmarshal([]byte(event.PayloadJSON), &block))
			if event.EventType == essessey.EventTypeContentBlockStart {
				starts = append(starts, block.Index)
			} else {
				stops = append(stops, block.Index)
			}
		case EventTypeProviderRetry:
			retries++
			assert.Equal(t, starts, stops, "close partial blocks before retry")
		}
	}
	assert.Equal(t, 1, retries)
	assert.Equal(t, []int{0, 1}, starts)
	assert.Equal(t, starts, stops)
}

func TestProviderStreamTurnExhaustsExistingLimit(t *testing.T) {
	t.Parallel()

	baseURL, requests := cutStreamServer(t, cutStreamChunk("partial", ""))
	base := newNativeRunnerFixture(t, cutStreamDriver(t, baseURL))
	_, err := base.runtime.Run(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "answer the request",
	})
	require.Error(t, err)
	assert.EqualValues(t, providerMaximumAttempts, requests.Load())
	assert.Contains(t, failedTurnError(t, base), "without a finish reason")
	messages, err := base.store.ListMessages(t.Context(),
		base.runtime.sessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	for _, message := range messages.Items {
		assert.NotEqual(t, models.MessageRoleAssistant, message.Role)
	}
	runs, err := base.store.ListModelRuns(t.Context(), base.runtime.sessionID,
		session.ListModelRunsOptions{})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1, "no workflow activity retry round")
	calls, err := base.store.ListModelCalls(t.Context(),
		base.runtime.sessionID, runs.Items[0].ID,
		session.ListModelCallsOptions{})
	require.NoError(t, err)
	require.Len(t, calls.Items, 1)
	assert.EqualValues(t, providerMaximumAttempts, calls.Items[0].TotalAttempts)
}

func TestProviderStreamMixedFailuresShareAttemptLimit(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter, _ *http.Request,
	) {
		if requests.Add(1)%2 == 1 {
			writer.WriteHeader(http.StatusInternalServerError)

			return
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		_, err := io.WriteString(writer, cutStreamChunk("partial", ""))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	driver, err := NewDriver(config.Upstream{
		Name: "cut", Type: config.UpstreamTypeOpenAI, BaseURL: server.URL,
	})
	require.NoError(t, err)
	base := newNativeRunnerFixture(t, driver)
	_, err = base.runtime.Run(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "answer the request",
	})
	require.Error(t, err)
	assert.EqualValues(t, providerMaximumAttempts, requests.Load())
	runs, err := base.store.ListModelRuns(t.Context(), base.runtime.sessionID,
		session.ListModelRunsOptions{})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	calls, err := base.store.ListModelCalls(t.Context(),
		base.runtime.sessionID, runs.Items[0].ID,
		session.ListModelCallsOptions{})
	require.NoError(t, err)
	require.Len(t, calls.Items, 1)
	assert.EqualValues(t, providerMaximumAttempts, calls.Items[0].TotalAttempts)
	var attempts []modelAuditRetryAttempt
	require.NoError(t, json.Unmarshal(
		[]byte(calls.Items[0].RetryAttemptsJSON), &attempts))
	require.Len(t, attempts, providerRetryCount)
	for index, attempt := range attempts {
		assert.Equal(t, index+1, attempt.Attempt)
	}
	var usage modelAuditUsage
	require.NoError(t, json.Unmarshal(
		[]byte(calls.Items[0].ResponseUsageJSON), &usage))
	require.Len(t, usage.FailedAttempts, providerMaximumAttempts)
	assert.Equal(t, providerMaximumAttempts,
		usage.FailedAttempts[providerRetryCount].Attempt)
}

func TestProviderStreamCompleteIsNotRetried(t *testing.T) {
	t.Parallel()

	baseURL, requests := cutStreamServer(t,
		cutStreamChunk("complete", "stop")+"data: [DONE]\n\n")
	var text string
	usage, err := cutStreamDriver(t, baseURL).Stream(t.Context(),
		cutStreamRequest(), func(delta elelem.Delta) error {
			text += delta.Text

			return nil
		})
	require.NoError(t, err)
	assert.Equal(t, "complete", text)
	assert.Equal(t, 1, usage.Retry.TotalAttempts)
	assert.EqualValues(t, 1, requests.Load())
}

func TestProviderStreamCallbackFailureIsNotRetried(t *testing.T) {
	t.Parallel()

	baseURL, requests := cutStreamServer(t,
		cutStreamChunk("complete", "stop")+"data: [DONE]\n\n")
	_, err := cutStreamDriver(t, baseURL).Stream(t.Context(),
		cutStreamRequest(), func(elelem.Delta) error {
			return io.ErrClosedPipe
		})
	require.ErrorIs(t, err, io.ErrClosedPipe)
	assert.EqualValues(t, 1, requests.Load())
}

func TestProviderStreamCancelledIsNotRetried(t *testing.T) {
	t.Parallel()

	baseURL, requests := cutStreamServer(t, cutStreamKeepAlive)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := cutStreamDriver(t, baseURL).Stream(ctx, cutStreamRequest(), nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, requests.Load())
}

func TestProviderStreamCutDiscardsReasoningAndToolFragments(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{
			name: "reasoning",
			body: strings.Replace(cutStreamChunk("partial", ""),
				`"content"`, `"reasoning_content"`, 1),
		},
		{
			name: "tool arguments",
			body: strings.Replace(cutStreamChunk("partial", ""),
				`"content":"partial"`,
				`"tool_calls":[{"index":0,"id":"fragment",`+
					`"type":"function","function":{"name":"example",`+
					`"arguments":"{"}}]`, 1),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			baseURL, requests := cutStreamServer(t, tc.body,
				cutStreamChunk("complete", "stop")+"data: [DONE]\n\n")
			base := newNativeRunnerFixture(t, cutStreamDriver(t, baseURL))
			result, err := base.runtime.Run(t.Context(), TurnRequest{
				RequestID: uuid.New(), Message: "answer the request",
			})
			require.NoError(t, err)
			assert.Equal(t, "complete", result.Text)
			messages, err := base.store.ListMessages(t.Context(),
				base.runtime.sessionID, session.ListMessagesOptions{})
			require.NoError(t, err)
			for _, message := range messages.Items {
				if message.Role == models.MessageRoleAssistant {
					assert.Equal(t, "complete", message.Content)
					assert.False(t, message.Incomplete)
					assert.JSONEq(t, "[]", message.ToolCallsJSON)
					assert.Empty(t, message.Thinking)
				}
			}
			assert.EqualValues(t, 2, requests.Load())
		})
	}
}
