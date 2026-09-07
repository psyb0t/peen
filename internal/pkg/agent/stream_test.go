package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/essessey/elelemstream"
	essesseysse "github.com/psyb0t/essessey/sse"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errStreamTestSecret stands in for an internal error carrying detail that
// must never reach a client's wire.
var errStreamTestSecret = errors.New("provider rejected key sk-live-abc123")

// Block indices and block transitions are elelemstream's job now, and it has
// its own tests upstream. What stays Peen's is the message envelope around
// them, so this checks the preamble actually opens the message.
func TestStartStreamWritesPreamble(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { require.NoError(t, reader.Close()) })

	prepared := &preparedTurn{
		opened: &session.OpenSessionResult{
			Session: &models.Session{ID: uuid.New()},
		},
		modelReference: "provider/model",
		turn:           &runtimeTurn{requestID: uuid.New()},
	}

	type readResult struct {
		content []byte
		err     error
	}

	result := make(chan error, 1)
	readResults := make(chan readResult, 1)

	go func() {
		content, err := io.ReadAll(reader)
		readResults <- readResult{content: content, err: err}
	}()

	go func() {
		_, err := startStream(context.Background(), writer, prepared)
		result <- err
	}()

	require.NoError(t, <-result)
	require.NoError(t, writer.Close())

	stream := <-readResults
	require.NoError(t, stream.err)
	assert.Contains(t, string(stream.content), essessey.EventTypeMessageStart)
	assert.Contains(t, string(stream.content), streamEventChatStatus)
}

// startStream must give the turn a publisher and an adapter, because the
// adapter is what produces content blocks once the provider starts answering.
func TestStartStreamAttachesTheAdapter(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { require.NoError(t, reader.Close()) })

	go func() { _, _ = io.Copy(io.Discard, reader) }()

	prepared := &preparedTurn{
		opened: &session.OpenSessionResult{
			Session: &models.Session{ID: uuid.New()},
		},
		modelReference: "provider/model",
		turn:           &runtimeTurn{requestID: uuid.New()},
	}

	_, err := startStream(context.Background(), writer, prepared)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	assert.NotNil(t, prepared.publisher)
	assert.NotNil(t, prepared.adapter)
	assert.NotNil(t, prepared.turn.statusReporter)
}

// Once headers are committed the JSON error envelope is unreachable, so a
// failing turn used to sever the connection with no diagnostic at all. The
// terminal frame is the only thing that tells a streaming client what went
// wrong, and it must say so without leaking the internal error chain.
func TestStreamStatusPublishesASanitizedTerminalError(t *testing.T) {
	buffer := &bytes.Buffer{}
	status := newBufferedStreamStatus(buffer)

	require.NoError(t, status.publishTerminalError(
		ctxerrors.Wrap(errStreamTestSecret, "/etc/secrets/provider-key.pem"),
	))

	events := readSSEEvents(t, essesseysse.NewSource(bytes.NewReader(
		buffer.Bytes(),
	)))
	require.Len(t, events, 1)
	assert.Equal(t, streamEventError, events[0].Event)

	payload := streamErrorPayload{}
	require.NoError(t, json.Unmarshal(events[0].Data, &payload))
	assert.Equal(t, streamEventError, payload.Type)
	assert.Equal(t, streamErrorTypeRequestFailed, payload.Error.Type)
	assert.Equal(t, streamErrorMessageRequestFailed, payload.Error.Message)

	body := buffer.String()
	assert.NotContains(t, body, errStreamTestSecret.Error())
	assert.NotContains(t, body, "provider-key.pem")
}

// The terminal classifications and their text are Chatz's, so a Chatz stream
// parser needs no translation. These were verified against the Chatz source
// rather than invented, after an earlier pass shipped a shape that did not
// match.
func TestClassifyStreamFailure(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		err         error
		wantType    string
		wantMessage string
	}{
		{
			name:        "a deadline is an upstream timeout",
			err:         context.DeadlineExceeded,
			wantType:    streamErrorTypeTimeout,
			wantMessage: streamErrorMessageTimeout,
		},
		{
			name:        "rate limiting",
			err:         commerr.ErrRateLimited,
			wantType:    streamErrorTypeRateLimited,
			wantMessage: streamErrorMessageRateLimited,
		},
		{
			name:        "an unavailable model",
			err:         ErrModelUnavailable,
			wantType:    streamErrorTypeModelUnavailable,
			wantMessage: streamErrorMessageModelUnavailable,
		},
		{
			name:        "a denied provider",
			err:         commerr.ErrPermissionDenied,
			wantType:    streamErrorTypeModelUnavailable,
			wantMessage: streamErrorMessageModelUnavailable,
		},
		{
			name:        "the provider context limit",
			err:         elelem.ErrContextExceeded,
			wantType:    streamErrorTypeContextLimit,
			wantMessage: streamErrorMessageContextLimit,
		},
		{
			name:        "compaction that could not free enough",
			err:         ErrCompactionUnavailable,
			wantType:    streamErrorTypeContextLimit,
			wantMessage: streamErrorMessageContextLimit,
		},
		{
			name:        "anything else",
			err:         errStreamTestSecret,
			wantType:    streamErrorTypeRequestFailed,
			wantMessage: streamErrorMessageRequestFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Wrapped, because a real failure arrives through several layers
			// and errors.Is has to still see through them.
			payload := classifyStreamFailure(
				ctxerrors.Wrap(tc.err, "run streamed agent turn"),
			)
			assert.Equal(t, streamEventError, payload.Type)
			assert.Equal(t, tc.wantType, payload.Error.Type)
			assert.Equal(t, tc.wantMessage, payload.Error.Message)
		})
	}
}

// The epilogue used to hardcode a clean end with zero output tokens, so a
// truncated answer and a tool-use round both reported as end_turn. A client
// that reads stop_reason to decide whether to ask for a continuation was being
// told the wrong thing on every turn.
func TestEpilogueStopReasonFollowsTheTurn(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		finishReason elelem.FinishReason
		hasToolCalls bool
		want         essessey.StopReason
	}{
		{
			name:         "a clean answer ends the turn",
			finishReason: elelem.FinishReasonStop,
			want:         essessey.StopReasonEndTurn,
		},
		{
			name:         "tool calls report tool use",
			finishReason: elelem.FinishReasonStop,
			hasToolCalls: true,
			want:         essessey.StopReasonToolUse,
		},
		{
			name:         "a truncated answer reports max tokens",
			finishReason: elelem.FinishReasonLength,
			want:         essessey.StopReasonMaxTokens,
		},
		{
			// Truncation wins. A cut-off answer that happened to contain tool
			// calls is still cut off, and calling it a clean tool round would
			// hide that from the client.
			name:         "truncation beats tool calls",
			finishReason: elelem.FinishReasonLength,
			hasToolCalls: true,
			want:         essessey.StopReasonMaxTokens,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, elelemstream.MapStopReason(
				tc.finishReason,
				tc.hasToolCalls,
			))
		})
	}
}

// The status frame repeats the event name in its body, as Chatz does.
func TestChatStatusPayloadCarriesItsType(t *testing.T) {
	buffer := &bytes.Buffer{}
	status := newBufferedStreamStatus(buffer)

	require.NoError(t, status.publish(streamStatusConnecting))

	events := readSSEEvents(t, essesseysse.NewSource(bytes.NewReader(
		buffer.Bytes(),
	)))
	require.Len(t, events, 1)

	payload := chatStatusPayload{}
	require.NoError(t, json.Unmarshal(events[0].Data, &payload))
	assert.Equal(t, streamEventChatStatus, payload.Type)
	assert.Equal(t, streamStatusConnecting, payload.Status)
}

// Cancellation is a normal end to a stream. Publishing an error for it would
// tell a client its own cancel was a failure.
func TestStreamStatusPublishesNoErrorForCancellation(t *testing.T) {
	buffer := &bytes.Buffer{}
	status := newBufferedStreamStatus(buffer)

	require.NoError(t, status.publishTerminalError(context.Canceled))
	assert.Empty(t, buffer.String())
}

// A repeated status is not resent, so a long stream does not carry one frame
// per delta.
func TestStreamStatusDeduplicates(t *testing.T) {
	buffer := &bytes.Buffer{}
	status := newBufferedStreamStatus(buffer)

	require.NoError(t, status.publish(streamStatusStreaming))
	require.NoError(t, status.publish(streamStatusStreaming))
	require.NoError(t, status.publish(streamStatusRunningTool))

	events := readSSEEvents(t, essesseysse.NewSource(bytes.NewReader(
		buffer.Bytes(),
	)))
	require.Len(t, events, 2)
	assert.Equal(t, []string{
		streamStatusStreaming,
		streamStatusRunningTool,
	}, chatStatuses(t, events))
}

// report reads both vocabularies: content blocks arrive under essessey's
// names from the adapter, tool and retry activity under Peen's own.
func TestStatusForEvent(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		eventType string
		want      string
	}{
		{
			name:      "a content block delta is streaming",
			eventType: essessey.EventTypeContentBlockDelta,
			want:      streamStatusStreaming,
		},
		{
			name:      "a tool call is running a tool",
			eventType: EventTypeToolUse,
			want:      streamStatusRunningTool,
		},
		{
			name:      "a provider retry is retrying",
			eventType: EventTypeProviderRetry,
			want:      streamStatusRetrying,
		},
		{
			name:      "a turn start advertises nothing",
			eventType: EventTypeTurnStarted,
		},
		{
			name:      "a message stop advertises nothing",
			eventType: essessey.EventTypeMessageStop,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, statusForEvent(tc.eventType))
		})
	}
}

func newBufferedStreamStatus(buffer *bytes.Buffer) *streamStatus {
	return &streamStatus{
		publisher: essessey.NewPublisher(
			context.Background(),
			essesseysse.NewWriterSink(buffer),
		),
	}
}
