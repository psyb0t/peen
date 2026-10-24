package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowEventPreservesNativeHistoryFormat(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		eventType  history.EventType
		attributes any
	}{
		{
			name: "workflow start", eventType: history.EventType_WorkflowExecutionStarted,
			attributes: &history.ExecutionStartedAttributes{Name: "agentExecutionV1"},
		},
		{
			name: "activity schedule", eventType: history.EventType_ActivityScheduled,
			attributes: &history.ActivityScheduledAttributes{Name: "modelRoundV1"},
		},
		{
			name: "activity result", eventType: history.EventType_ActivityCompleted,
			attributes: &history.ActivityCompletedAttributes{},
		},
		{
			name: "cancellation", eventType: history.EventType_WorkflowExecutionCanceled,
			attributes: &history.ExecutionCanceledAttributes{},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			event := history.NewPendingEvent(
				time.Now().UTC(), tc.eventType, tc.attributes,
				history.ScheduleEventID(1),
			)
			encoded, err := json.Marshal(event)
			require.NoError(t, err)
			var decoded workflowEvent
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			assert.Equal(t, event, decoded.Event)
			roundTrip, err := json.Marshal(decoded)
			require.NoError(t, err)
			assert.JSONEq(t, string(encoded), string(roundTrip))
		})
	}
}

func TestWorkflowEventRejectsMalformedHistoryWithoutPanic(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		input string
	}{
		{name: "null", input: "null"},
		{name: "empty", input: "{}"},
		{name: "array", input: "[]"},
		{name: "wrong identity", input: `{"id":"not-an-id"}`},
		{name: "missing attributes", input: `{"id":"00000000-0000-0000-0000-000000000001","t":1,"ts":"2000-01-01T00:00:00Z"}`},
		{name: "null attributes", input: `{"id":"00000000-0000-0000-0000-000000000001","t":1,"ts":"2000-01-01T00:00:00Z","attr":null}`},
		{name: "unknown type", input: `{"id":"00000000-0000-0000-0000-000000000001","t":999,"ts":"2000-01-01T00:00:00Z","attr":{}}`},
		{name: "negative sequence", input: `{"id":"00000000-0000-0000-0000-000000000001","sid":-1,"t":1,"ts":"2000-01-01T00:00:00Z","attr":{}}`},
		{name: "attribute type mismatch", input: `{"id":"00000000-0000-0000-0000-000000000001","t":1,"ts":"2000-01-01T00:00:00Z","attr":{"name":12}}`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var decoded workflowEvent
			require.NotPanics(t, func() {
				err := json.Unmarshal([]byte(tc.input), &decoded)
				require.ErrorIs(t, err, commerr.ErrValidationFailed)
			})
			assert.Nil(t, decoded.Event)
		})
	}
}
