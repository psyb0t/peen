package agent

import (
	"bytes"
	"context"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The SSE contract is a compatibility contract: a Chatz adapter must reuse
// Chatz's existing stream parser without translating anything. Every other
// test in this package asserts Peen's behavior back to itself, which cannot
// catch a divergence from Chatz, and did not: three separate mismatches
// shipped behind green tests and were only found by reading Chatz's source.
//
// These are Chatz's own golden values, copied verbatim from its
// internal/pkg/core/chats/turn_status_test.go and stream_failure_test.go.
// Depending on Chatz itself is not an option, one service does not import
// another, so pinning its literals here is the closest thing to the real
// check. If Chatz changes these, this test is where it should hurt.
const (
	chatzGoldenConnecting = `{"type":"chat_status","status":"connecting"}`

	chatzGoldenRequestFailed = `{
		"type": "error",
		"error": {
			"type": "request_failed",
			"message": "The model request failed. Try again."
		}
	}`

	chatzGoldenContextLimit = `{
		"type": "error",
		"error": {
			"type": "context_limit",
			"message": "This conversation is too large for the selected model."
		}
	}`
)

func TestChatzGoldenChatStatusFrame(t *testing.T) {
	t.Parallel()

	sink := essessey.NewInMemorySink()
	status := &streamStatus{
		publisher: essessey.NewPublisher(t.Context(), sink),
	}

	require.NoError(t, status.publish(streamStatusConnecting))

	events := sink.Events()
	require.Len(t, events, 1)
	assert.Equal(t, streamEventChatStatus, events[0].Event)
	assert.JSONEq(t, chatzGoldenConnecting, string(events[0].Data))
}

func TestChatzGoldenTerminalErrorFrames(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		err    error
		golden string
	}{
		{
			name:   "an unclassified failure",
			err:    commerr.ErrInvalidState,
			golden: chatzGoldenRequestFailed,
		},
		{
			name:   "a provider context limit",
			err:    elelem.ErrContextExceeded,
			golden: chatzGoldenContextLimit,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := essessey.NewInMemorySink()
			status := &streamStatus{
				publisher: essessey.NewPublisher(t.Context(), sink),
			}

			require.NoError(t, status.publishTerminalError(
				ctxerrors.Wrap(tc.err, "run streamed agent turn"),
			))

			events := sink.Events()
			require.Len(t, events, 1)
			assert.Equal(t, streamEventError, events[0].Event)
			assert.JSONEq(t, tc.golden, string(events[0].Data))
		})
	}
}

// Cancelling is a normal end to a stream in Chatz too: its streamFailure
// returns false for context.Canceled and publishes nothing.
func TestChatzGoldenCancellationPublishesNothing(t *testing.T) {
	t.Parallel()

	sink := essessey.NewInMemorySink()
	status := &streamStatus{
		publisher: essessey.NewPublisher(t.Context(), sink),
	}

	require.NoError(t, status.publishTerminalError(context.Canceled))
	assert.Empty(t, sink.Events())
}

// The seven content-block event names are essessey's, and a Chatz decoder
// keys off them. Pinning the set here means adding an eighth type, or renaming
// one, cannot pass unnoticed.
func TestChatzGoldenContentBlockEventNames(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []essessey.EventType{
		"message_start",
		"content_block_start",
		"ping",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}, []essessey.EventType{
		essessey.EventTypeMessageStart,
		essessey.EventTypeContentBlockStart,
		essessey.EventTypePing,
		essessey.EventTypeContentBlockDelta,
		essessey.EventTypeContentBlockStop,
		essessey.EventTypeMessageDelta,
		essessey.EventTypeMessageStop,
	})
}

// A frame must not carry an SSE id: field. The plan forbids adding one in the
// first release, and a Chatz parser does not expect it.
func TestChatzGoldenFramesCarryNoEventID(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	status := newBufferedStreamStatus(buffer)

	require.NoError(t, status.publish(streamStatusStreaming))

	assert.NotContains(t, buffer.String(), "id:")
}
