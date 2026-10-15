package agent

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/essessey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamOffsetPreservesExactPayloadValues(t *testing.T) {
	t.Parallel()
	data := json.RawMessage(`{"index":2,"value":9007199254740993}`)
	shifted, err := offsetWorkflowStreamEvent(essessey.Event{
		Event: essessey.EventTypeContentBlockStart, Data: data,
	}, 5)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(shifted.Data, &fields))
	assert.Equal(t, "7", string(fields["index"]))
	assert.Equal(t, "9007199254740993", string(fields["value"]))
	assert.Equal(t, `{"index":2,"value":9007199254740993}`, string(data))
	unchanged, err := offsetWorkflowStreamEvent(essessey.Event{
		Event: essessey.EventTypePing, Data: data,
	}, 5)
	require.NoError(t, err)
	assert.Equal(t, data, unchanged.Data)
}

func TestStreamOffsetRejectsInvalidIndex(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name string
		data string
		base int
	}{
		{name: "missing", data: `{}`},
		{name: "null", data: `{"index":null}`},
		{name: "negative", data: `{"index":-1}`},
		{name: "negative offset", data: `{"index":0}`, base: -1},
		{name: "overflow", data: `{"index":1}`, base: math.MaxInt - 1},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := offsetWorkflowStreamEvent(essessey.Event{
				Event: essessey.EventTypeContentBlockStart,
				Data:  json.RawMessage(tc.data),
			}, tc.base)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

type rejectedWorkflowStreamSink struct{}

func (rejectedWorkflowStreamSink) Emit(
	context.Context,
	essessey.Event,
) error {
	return commerr.ErrFetchFailed
}

func TestStreamProjectionRetainsPositionOnPublicationError(t *testing.T) {
	t.Parallel()
	target := essessey.NewPublisher(t.Context(), rejectedWorkflowStreamSink{})
	projection := newWorkflowStreamProjection(t.Context(), target, 7)
	err := projection.publisher.SendContentBlockStartText(0)
	require.ErrorIs(t, err, commerr.ErrFetchFailed)
	assert.Equal(t, 7, projection.nextIndex())
}

func TestStreamOffsetReturnsJSONDecodeErrors(t *testing.T) {
	t.Parallel()
	t.Run("malformed", func(t *testing.T) {
		t.Parallel()
		_, err := offsetWorkflowStreamEvent(essessey.Event{
			Event: essessey.EventTypeContentBlockStart,
			Data:  json.RawMessage(`{"index":`),
		}, 0)
		var syntax *json.SyntaxError
		require.ErrorAs(t, err, &syntax)
	})
	t.Run("noninteger", func(t *testing.T) {
		t.Parallel()
		_, err := offsetWorkflowStreamEvent(essessey.Event{
			Event: essessey.EventTypeContentBlockStart,
			Data:  json.RawMessage(`{"index":"invalid"}`),
		}, 0)
		var mismatch *json.UnmarshalTypeError
		require.ErrorAs(t, err, &mismatch)
	})
}
