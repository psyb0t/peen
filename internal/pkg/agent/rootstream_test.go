package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootStreamContinuesIndexesAfterColdTurnRestoration(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Thinking("first reasoning", "first answer"),
		elelemtest.Thinking("second reasoning", "second answer"),
	)
	fixture, original := newRootModelWorkflowFixture(t, driver)
	first, err := fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	fixture.base.store.ReleaseTurn(original.lease)
	input := fixture.input
	input.Context = first.Output.Context
	holder := session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	restored, _, err := fixture.base.runtime.restoreRootWorkflowTurn(t.Context(),
		input, holder, nil)
	require.NoError(t, err)
	fixture.engine.options.GenerationID = holder.OwnerGenerationID
	fixture.engine.options.ModelRound = func(
		ctx context.Context, round workflowRoundInput,
	) (workflowRoundResult, error) {
		return fixture.base.runtime.workflowRootModelRound(ctx, restored, round)
	}
	_, err = fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: input, Round: 1,
	})
	require.NoError(t, err)
	page, err := fixture.base.store.ListEvents(t.Context(), input.SessionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	indexes := make([]int, 0, len(page.Items))
	for _, event := range page.Items {
		if event.EventType != essessey.EventTypeContentBlockStart {
			continue
		}

		var block essessey.ContentBlockStartData
		require.NoError(t, json.Unmarshal([]byte(event.PayloadJSON), &block))
		indexes = append(indexes, block.Index)
	}
	assert.Equal(t, []int{0, 1, 2, 3}, indexes)
	assert.Equal(t, 2, driver.Calls())
}

func TestRootStreamRestoresToolSlotsAfterCompletedModel(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Turn{
		Deltas: []elelem.Delta{{Reasoning: "inspect example"}, {
			ToolCall: &elelem.ToolCallDelta{
				Index: 0, ID: runtimeToolCallID, Name: toolNameReadFile,
				Arguments: runtimeToolArguments(t, runtimeToolFileName),
			}, FinishReason: elelem.FinishReasonToolCalls,
		}},
	})
	fixture, original := newRootModelWorkflowFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace,
		runtimeToolFileName), runtimeToolFileBody)
	first, err := fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	fixture.base.store.ReleaseTurn(original.lease)
	input := fixture.input
	input.Context = first.Output.Context
	holder := session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	restored, scope, err := fixture.base.runtime.restoreRootWorkflowTurn(
		t.Context(), input, holder, nil)
	require.NoError(t, err)
	fixture.engine.options.GenerationID = holder.OwnerGenerationID
	fixture.engine.options.ToolCall = func(
		ctx context.Context, call workflowToolInput,
	) (workflowToolResult, error) {
		return fixture.base.runtime.workflowRootToolCall(ctx, restored, scope,
			call)
	}
	_, err = fixture.engine.toolCall(t.Context(), workflowToolInput{
		Round: workflowRoundInput{Execution: input}, Call: first.Output.Calls[0],
	})
	require.NoError(t, err)
	assert.Equal(t, []int{0, 1, 2}, rootStreamIndexes(t, fixture))
	assert.Equal(t, 1, driver.Calls())
}

func rootStreamIndexes(t *testing.T, fixture executionLoopFixture) []int {
	t.Helper()
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	page, err := fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{
			TurnID:     execution.RootTurnID,
			EventTypes: []string{essessey.EventTypeContentBlockStart},
		})
	require.NoError(t, err)
	indexes := make([]int, 0, len(page.Items))
	for _, event := range page.Items {
		var block essessey.ContentBlockStartData
		require.NoError(t, json.Unmarshal([]byte(event.PayloadJSON), &block))
		indexes = append(indexes, block.Index)
	}

	return indexes
}

func TestRootStreamClosesRetainedPartialBlockBeforeNewOutput(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("new output"))
	fixture, original := newRootModelWorkflowFixture(t, driver)
	require.NoError(t, original.publisher.SendContentBlockStartText(0))
	require.NoError(t, original.publisher.SendContentBlockDeltaText(0,
		"interrupted output"))
	fixture.base.store.ReleaseTurn(original.lease)
	holder := session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	restored, _, err := fixture.base.runtime.restoreRootWorkflowTurn(t.Context(),
		fixture.input, holder, nil)
	require.NoError(t, err)
	fixture.engine.options.GenerationID = holder.OwnerGenerationID
	fixture.engine.options.ModelRound = func(
		ctx context.Context, input workflowRoundInput,
	) (workflowRoundResult, error) {
		return fixture.base.runtime.workflowRootModelRound(ctx, restored, input)
	}
	_, err = fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	assert.Equal(t, []int{0, 1}, rootStreamIndexes(t, fixture))
	position, err := restored.readWorkflowStreamPosition(t.Context())
	require.NoError(t, err)
	assert.Empty(t, position.open)
	assert.Equal(t, 2, position.next)
	messages, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, messages.Items, 2)
	assert.Equal(t, "new output", messages.Items[1].Content)
	assert.Equal(t, 1, driver.Calls())
}
