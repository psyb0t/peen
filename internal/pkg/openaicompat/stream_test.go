package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func emitText(request control.ConversationRequest, id uuid.UUID,
	text string,
) error {
	event := streamEvent{}
	event.Delta.Type = textDelta
	event.Delta.Text = text
	payload, err := json.Marshal(event)
	if err != nil {
		return ctxerrors.Wrap(err, "encode test text delta")
	}

	return request.OnEvent(session.FeedItem{
		SessionID: id, Kind: session.FeedKindEvent,
		EventType: contentBlockDelta, PayloadJSON: string(payload),
	})
}

func TestCompletionStreamsTextFinalUsageAndDone(t *testing.T) {
	t.Parallel()

	result := testResult()
	h := testHandler(t, testDependencies(func(
		_ context.Context,
		request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		require.NotNil(t, request.OnEvent)
		require.NoError(t, emitText(request, result.SessionID, "Hel"))
		require.NoError(t, emitText(request, result.SessionID, "lo\nthere"))

		return result, nil
	}))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, postRequest(t.Context(), `{"stream":true,"messages":[
		{"role":"user","content":"Hello"}]}`))

	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
	require.True(t, response.Flushed)
	assert.Equal(t, result.SessionID.String(),
		response.Result().Header.Get(headerSessionID))
	chunks := streamChunks(t, response.Body.String())
	require.Len(t, chunks, 4)
	assert.Equal(t, roleAssistant, chunks[0].Choices[0].Delta.Role)
	assert.Equal(t, "Hel", *chunks[1].Choices[0].Delta.Content)
	assert.Equal(t, "lo\nthere", *chunks[2].Choices[0].Delta.Content)
	assert.Equal(t, finishStop, *chunks[3].Choices[0].FinishReason)
	require.NotNil(t, chunks[3].Usage)
	assert.Equal(t, int64(10), chunks[3].Usage.TotalTokens)
	for _, event := range chunks {
		assert.Equal(t, chunks[0].ID, event.ID)
		assert.Equal(t, objectChunk, event.Object)
	}
}

func streamChunks(t *testing.T, body string) []chunk {
	t.Helper()

	frames := strings.Split(strings.TrimSuffix(body, sseEnd), sseEnd)
	require.NotEmpty(t, frames)
	require.Equal(t, ssePrefix+sseDone, frames[len(frames)-1])
	chunks := make([]chunk, 0, len(frames)-1)

	for _, frame := range frames[:len(frames)-1] {
		require.True(t, strings.HasPrefix(frame, ssePrefix))

		var event chunk
		require.NoError(t, json.Unmarshal([]byte(
			strings.TrimPrefix(frame, ssePrefix)), &event))
		require.Len(t, event.Choices, 1)
		chunks = append(chunks, event)
	}

	return chunks
}

func TestStreamFailureUsesSafeExtensionAfterFirstWrite(t *testing.T) {
	t.Parallel()

	h := testHandler(t, testDependencies(func(
		_ context.Context,
		request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		if err := emitText(request, uuid.New(), "Partial"); err != nil {
			return nil, err
		}

		return nil, ctxerrors.Wrap(commerr.ErrExecFailed,
			"/private/provider EXAMPLE-TOKEN")
	}))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, postRequest(t.Context(), `{"stream":true,"messages":[
		{"role":"user","content":"Hello"}]}`))

	chunks := streamChunks(t, response.Body.String())
	require.Len(t, chunks, 3)
	last := chunks[len(chunks)-1]
	assert.Equal(t, finishError, *last.Choices[0].FinishReason)
	require.NotNil(t, last.Extension)
	assert.Equal(t, codeTurnFailed, last.Extension.Error.Code)
	assert.NotContains(t, response.Body.String(), "/private/provider")
	assert.NotContains(t, response.Body.String(), "EXAMPLE-TOKEN")
}

func TestClientDisconnectCancelsRunnerContext(t *testing.T) {
	t.Parallel()

	cancelled := make(chan struct{})
	h := testHandler(t, testDependencies(func(
		ctx context.Context,
		request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		if err := emitText(request, uuid.New(), "Started"); err != nil {
			return nil, err
		}

		<-ctx.Done()
		close(cancelled)

		return nil, ctxerrors.Wrap(ctx.Err(), "test runner cancelled")
	}))
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		server.URL+completionsPath, strings.NewReader(
			`{"stream":true,"messages":[{"role":"user","content":"Hello"}]}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	cancel()
	require.NoError(t, response.Body.Close())

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel the turn runner")
	}
}

func TestStreamIgnoresChildAndNonTextEvents(t *testing.T) {
	t.Parallel()

	result := testResult()
	response := httptest.NewRecorder()
	h := testHandler(t, testDependencies(func(
		_ context.Context,
		request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		require.NoError(t, request.OnEvent(session.FeedItem{
			Kind:        session.FeedKindAgentRunEvent,
			EventType:   contentBlockDelta,
			PayloadJSON: `{"delta":{"type":"text_delta","text":"CHILD"}}`,
		}))
		require.NoError(t, request.OnEvent(session.FeedItem{
			Kind: session.FeedKindEvent, SessionID: result.SessionID,
			EventType:   contentBlockDelta,
			PayloadJSON: `{"delta":{"type":"thinking_delta","text":"HIDDEN"}}`,
		}))
		assert.Empty(t, response.Body.String())

		return result, nil
	}))
	h.ServeHTTP(response, postRequest(t.Context(), `{"stream":true,"messages":[
		{"role":"user","content":"Hello"}]}`))

	assert.NotContains(t, response.Body.String(), "CHILD")
	assert.NotContains(t, response.Body.String(), "HIDDEN")
	chunks := streamChunks(t, response.Body.String())
	require.Len(t, chunks, 3)
	assert.Equal(t, result.Text, *chunks[1].Choices[0].Delta.Content)
}
