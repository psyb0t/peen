package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootStartEventsRetainRequestAndSourceIdentity(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		deferred bool
		wantType string
	}{
		{name: "new message", wantType: EventTypeUserMessageCreated},
		{
			name: "accepted message", deferred: true,
			wantType: EventTypeUserMessageDelivered,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			if tc.deferred {
				ctx = context.WithValue(ctx, deferredRootContextKey{},
					&deferredRootInput{})
			}

			input := TurnRequest{
				RequestID: uuid.New(), SourceEventID: uuid.New(),
				Message: "saved input",
			}
			events, records, err := rootStartEvents(ctx, input, input.RequestID,
				[]skillMessage{{name: "example", text: "frozen instructions"}})
			require.NoError(t, err)
			require.Len(t, events, 2)
			require.Len(t, records, 2)
			assert.Equal(t, tc.wantType, events[0].Type)
			assert.Equal(t, tc.wantType, records[0].EventType)
			assert.Equal(t, input.RequestID, records[0].RequestID)
			assert.Equal(t, input.SourceEventID, events[0].TriggeringEventID)
			assert.Equal(t, EventTypeSkillInjected, records[1].EventType)
		})
	}
}
