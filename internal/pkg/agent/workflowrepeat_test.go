package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepeatedToolCallsSurviveSavedContext(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		toolNameReadFile, toolNameWaitJob,
		toolNameReadJobOutput, toolNameWaitMessages,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			conversation := workflowConversation{}
			calls := 0
			tool := elelem.Tool{Name: name, Handler: func(
				_ context.Context, _ elelem.ToolInput,
			) (elelem.ToolResult, error) {
				calls++

				return elelem.ToolResult{Content: "same"}, nil
			}}
			for attempt := 1; attempt <= 6; attempt++ {
				encoded, err := json.Marshal(conversation)
				require.NoError(t, err)
				input := workflowToolInput{Call: elelem.ToolCall{
					ID: "call", Name: name,
					Arguments: json.RawMessage(`{"path":"a","limit":2}`),
				}, Round: workflowRoundInput{Execution: workflowExecutionInput{
					Context: encoded,
				}}}
				if attempt%2 == 0 {
					input.Call.Arguments = json.RawMessage(
						`{ "limit":2, "path":"a" }`)
				}

				result, err := runWorkflowTool(t.Context(),
					elelem.NewToolSet(tool), input, workflowToolOptions{})
				require.NoError(t, err)
				exempt := repeatExemptTool(name)
				assert.Equal(t, attempt >= 5 && !exempt, result.Result.IsError)
				assert.Equal(t, attempt == 3 && !exempt,
					strings.Contains(result.Result.Content, repeatWarning))
				if attempt >= 5 && !exempt {
					assert.Contains(t, result.Result.Content, repeatRefusal)
				}

				retainWorkflowRepeat(&conversation, result.Repeat)
				// Recreate state from durable JSON, without retaining the wrapper.
				encoded, err = json.Marshal(conversation)
				require.NoError(t, err)
				conversation = workflowConversation{}
				require.NoError(t, json.Unmarshal(encoded, &conversation))
			}

			if repeatExemptTool(name) {
				assert.Equal(t, 6, calls)
			} else {
				assert.Equal(t, 4, calls)
			}
		})
	}
}

func TestRepeatHashUsesFinalPostHookResult(t *testing.T) {
	t.Parallel()
	for _, phased := range []bool{false, true} {
		t.Run(fmt.Sprintf("phased=%t", phased), func(t *testing.T) {
			t.Parallel()
			conversation := workflowConversation{}
			count := 0
			input := phaseExecutionTestInput(t).Tool
			tool := elelem.Tool{
				Name: input.Call.Name,
				Handler: func(context.Context, elelem.ToolInput) (
					elelem.ToolResult, error,
				) {
					return elelem.ToolResult{Content: "unchanged handler"}, nil
				},
				PostRun: func(_ context.Context, event *elelem.ToolEvent) error {
					count++
					event.Result.Content = fmt.Sprintf("post result %d", count)

					return nil
				},
			}
			for range 6 {
				encoded, err := json.Marshal(conversation)
				require.NoError(t, err)
				input.Round.Execution.Context = encoded
				var result workflowToolResult
				if phased {
					result = repeatedToolPhases(t, tool, input)
				} else {
					result, err = runWorkflowTool(t.Context(),
						elelem.NewToolSet(tool), input, workflowToolOptions{})
					require.NoError(t, err)
				}

				assert.False(t, result.Result.IsError)
				assert.NotContains(t, result.Result.Content, repeatWarning)
				assert.Equal(t, 1, result.Repeat.Count)
				retainWorkflowRepeat(&conversation, result.Repeat)
			}

			assert.Equal(t, 6, count)
		})
	}
}

func repeatedToolPhases(t *testing.T, tool elelem.Tool,
	input workflowToolInput,
) workflowToolResult {
	t.Helper()
	state := workflowToolPhaseState{}
	for _, phase := range []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseHandler,
		workflowToolPhaseOutcome, workflowToolPhasePost, workflowToolPhaseProject,
	} {
		output, err := runWorkflowToolPhase(t.Context(), elelem.NewToolSet(tool),
			workflowToolPhaseInput{Tool: input, State: state, Phase: phase},
			workflowPhaseExecutionOptions{})
		require.NoError(t, err)
		encoded, err := json.Marshal(output)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(encoded, &state))
	}

	return state.Outcome
}

func TestRepeatWarningKeepsOutputWrapper(t *testing.T) {
	t.Parallel()
	conversation := workflowConversation{}
	input := phaseExecutionTestInput(t).Tool
	input.Call.Name = toolNameReadFile
	tool := elelem.Tool{
		Name: toolNameReadFile,
		Handler: func(context.Context, elelem.ToolInput) (
			elelem.ToolResult, error,
		) {
			return elelem.ToolResult{Content: `{
"content":"     1\tdata\n[lines 1-1 of 1]",
"firstLine":1,"totalLines":1
}`}, nil
		},
		PostRun: func(ctx context.Context, event *elelem.ToolEvent) error {
			content, err := markToolOutput(ctx, event.Tool.Name,
				event.Result.Content, 0)
			event.Result.Content = content

			return err
		},
	}
	for attempt := 1; attempt <= 3; attempt++ {
		encoded, err := json.Marshal(conversation)
		require.NoError(t, err)
		input.Round.Execution.Context = encoded
		outcome, err := runWorkflowTool(t.Context(), elelem.NewToolSet(tool),
			input, workflowToolOptions{MaxResultTokens: 200})
		require.NoError(t, err)
		_, inner, closing, wrapped := splitToolOutput(outcome.Result.Content)
		require.True(t, wrapped)
		assert.Contains(t, closing, toolOutputCloseTag)
		assert.Equal(t, attempt == 3, strings.Contains(inner, repeatWarning))
		retainWorkflowRepeat(&conversation, outcome.Repeat)
	}
}

func TestRepeatCheckAfterSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	fixture.input.MaxRounds = 8
	count := 0
	tool := elelem.Tool{Name: toolNameReadFile, Handler: func(
		_ context.Context, _ elelem.ToolInput,
	) (elelem.ToolResult, error) {
		count++

		return elelem.ToolResult{Content: "unchanged"}, nil
	}}
	fixture.engine.options.ToolPhase = func(ctx context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		state := input.State
		if input.Phase == workflowToolPhaseHandler {
			outcome, err := runWorkflowTool(ctx, elelem.NewToolSet(tool),
				input.Tool, workflowToolOptions{})
			state.Outcome = outcome

			return state, err
		}

		encoded, err := workflowMergeRound(input.Tool.Round.Execution.Context,
			[]workflowToolInput{input.Tool}, []workflowToolResult{state.Outcome})
		state.Outcome.Context = encoded

		return state, err
	}
	for attempt := range 5 {
		if attempt == 2 {
			fixture.stop()
			require.NoError(t, fixture.base.handle.Close())
			handle, err := db.Open(t.Context(), db.Config{
				Directory: fixture.base.stateDirectory,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, handle.Close()) })
			store, err := session.NewStore(handle, session.Options{})
			require.NoError(t, err)
			fixture.engine.options.Store = store
			fixture.engine.options.GenerationID = uuid.New()
			fixture.engine.options.Lifecycle = t.Context()
			page, err := store.ListExecutionActivities(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID,
				session.ListEventsOptions{})
			require.NoError(t, err)
			found := false
			for _, activity := range page.Items {
				if activity.StepID == "tool:1:0:project" {
					fixture.input.Context = json.RawMessage(activity.ContextJSON)
					found = true
				}
			}

			require.True(t, found)
		}

		input := toolPhaseTestInput(fixture)
		input.Tool.Compact = true
		input.Tool.Round.Round = attempt
		input.Tool.Call.ID = uuid.NewString()
		for _, phase := range []workflowToolPhase{
			workflowToolPhaseHandler, workflowToolPhaseProject,
		} {
			input.Phase = phase
			result, err := fixture.engine.toolPhase(t.Context(), input)
			require.NoError(t, err)
			input.State = result.Output
		}

		fixture.input.Context = input.State.Outcome.Context
		assert.Equal(t, attempt == 2,
			strings.Contains(input.State.Outcome.Result.Content, repeatWarning))
		assert.Equal(t, attempt == 4, input.State.Outcome.Result.IsError)
	}

	assert.Equal(t, 4, count)
}

func TestChangedToolResultResetsRepetition(t *testing.T) {
	t.Parallel()
	conversation := workflowConversation{}
	key := "key"
	for _, hash := range []string{"a", "a", "b", "b"} {
		retainWorkflowRepeat(&conversation, &workflowRepeatResult{
			Key: key, workflowRepeatCount: workflowRepeatCount{Hash: hash, Count: 1},
		})
	}

	assert.Equal(t, "b", conversation.Repeats[key].Hash)
	assert.Equal(t, 2, conversation.Repeats[key].Count)
	assert.Equal(t, map[string]int{"a": 2, "b": 2},
		conversation.Repeats[key].Results)
}

func TestRepeatedCallsWithinParallelRoundShareSavedCounts(t *testing.T) {
	t.Parallel()
	conversation := workflowConversation{Limits: &workflowExecutionLimits{
		CompactTools: true,
	}}
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	round := workflowRoundInput{Execution: workflowExecutionInput{
		Context: encoded, MaxConcurrentTools: 6,
	}}
	call := elelem.ToolCall{
		Name:      toolNameReadFile,
		Arguments: json.RawMessage(`{"path":"a"}`),
	}
	calls := []elelem.ToolCall{call, call, call, call, call, call}
	assert.Equal(t, 1, workflowRepeatBatchEnd(round, calls, 0))
	key := repeatCallKey(call)
	for range 4 {
		round, err = advanceWorkflowRepeatRound(round, []workflowToolResult{{
			Repeat: &workflowRepeatResult{
				Key:                 key,
				workflowRepeatCount: workflowRepeatCount{Hash: "same", Count: 1},
			},
		}})
		require.NoError(t, err)
	}

	require.NoError(t, json.Unmarshal(round.Execution.Context, &conversation))
	assert.Equal(t, 4, conversation.Repeats[key].Count)
}
