package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type phaseStreamCapture struct {
	mutex  sync.Mutex
	events []essessey.Event
}

func (s *phaseStreamCapture) Emit(_ context.Context, event essessey.Event) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.events = append(s.events, event)

	return nil
}

func TestPhaseStreamProjectionRetainsPreHookAttempt(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(workflowConversation{
		Stream: &workflowToolStream{Base: 9, Count: 1},
		Messages: []elelem.Message{{
			Role:      elelem.RoleAssistant,
			ToolCalls: []elelem.ToolCall{{ID: runtimeToolCallID}},
		}},
	})
	require.NoError(t, err)
	ctx := context.WithValue(t.Context(), workflowActivityLeaseContextKey{},
		session.ActivityLease{Attempt: 1})
	sink := &phaseStreamCapture{}
	prepared := &preparedTurn{publisher: essessey.NewPublisher(ctx, sink)}
	adapter, err := prepared.workflowPhaseToolAdapter(ctx, workflowToolPhaseInput{
		Tool: workflowToolInput{Round: workflowRoundInput{
			Execution: workflowExecutionInput{Context: encoded},
		}},
		State: workflowToolPhaseState{StreamAttempt: 3},
	})
	require.NoError(t, err)
	require.Empty(t, sink.events)
	require.NoError(t, adapter.OnToolResult(ctx, elelem.ToolCallEvent{
		CallID: runtimeToolCallID, Result: &elelem.ToolResult{Content: "saved"},
	}))
	starts := []int{}
	for _, event := range sink.events {
		if event.Event != essessey.EventTypeContentBlockStart {
			continue
		}

		var block struct {
			Index int `json:"index"`
		}
		require.NoError(t, json.Unmarshal(event.Data, &block))
		starts = append(starts, block.Index)
	}
	assert.Equal(t, []int{14}, starts)
}
