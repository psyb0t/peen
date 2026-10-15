package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func phaseExecutionTestInput(t *testing.T) workflowToolPhaseInput {
	t.Helper()
	tool := workflowToolTestInput()
	tool.Round.Execution.SessionID = uuid.New()
	tool.Round.Execution.ExecutionID = uuid.New()
	tool.Round.Execution.MaxRounds = 4
	tool.Round.Execution.MaxConcurrentTools = 1

	return workflowToolPhaseInput{Tool: tool, Phase: workflowToolPhasePre}
}

func runPhaseExecutionTest(
	t *testing.T,
	tool elelem.Tool,
) workflowToolPhaseState {
	t.Helper()
	input := phaseExecutionTestInput(t)
	for _, phase := range []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseHandler,
		workflowToolPhaseOutcome, workflowToolPhasePost, workflowToolPhaseProject,
	} {
		if input.State.Finished && phase != workflowToolPhaseProject {
			continue
		}

		input.Phase = phase
		output, err := runWorkflowToolPhase(t.Context(), elelem.NewToolSet(tool),
			input, workflowPhaseExecutionOptions{})
		require.NoError(t, err)
		encoded, err := json.Marshal(output)
		require.NoError(t, err)
		var saved workflowToolPhaseState
		require.NoError(t, json.Unmarshal(encoded, &saved))
		input.State = saved
	}

	return input.State
}

func TestPhaseExecutionRetainsLifecycleAcrossJSON(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		handlerErr error
		wantPhase  elelem.ToolPhase
	}{
		{name: "success", wantPhase: elelem.ToolPhaseOnSuccess},
		{
			name: "handler failure", handlerErr: commerr.ErrPermissionDenied,
			wantPhase: elelem.ToolPhaseOnError,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			trace := []string{}
			hook := func(_ context.Context, event *elelem.ToolEvent) error {
				trace = append(trace, event.Phase)
				if event.Phase == elelem.ToolPhasePreRun {
					event.RawArguments = json.RawMessage(`{"retained":true}`)
				} else {
					assert.JSONEq(t, `{"retained":true}`, string(event.RawArguments))
					assert.Equal(t, tc.handlerErr != nil, event.Err != nil)
				}

				if event.Phase == elelem.ToolPhasePostRun {
					event.Result.Content = "rewritten"
				}

				return nil
			}
			inject := func(_ context.Context, event *elelem.ToolEvent) (
				*elelem.MessageInjection, error,
			) {
				trace = append(trace, "injection")

				return &elelem.MessageInjection{
					Type: elelem.RoleUser, Content: event.Phase,
				}, nil
			}
			state := runPhaseExecutionTest(t, elelem.Tool{
				Name: workflowToolTestName, PreRun: hook,
				OnSuccess: hook, OnError: hook, PostRun: hook,
				OnSuccessMessageInjector: inject, OnErrorMessageInjector: inject,
				PostRunMessageInjector: inject,
				Handler: func(_ context.Context, input elelem.ToolInput) (
					elelem.ToolResult, error,
				) {
					trace = append(trace, "handler")
					assert.JSONEq(t, `{}`, string(input.Arguments))

					return elelem.ToolResult{Content: "original"}, tc.handlerErr
				},
			})
			assert.Equal(t, []string{
				elelem.ToolPhasePreRun, "handler",
				tc.wantPhase, "injection", elelem.ToolPhasePostRun, "injection",
			}, trace)
			assert.Equal(t, "rewritten", state.Outcome.Result.Content)
			assert.Equal(t, tc.handlerErr != nil, state.Outcome.Result.IsError)
			assert.Empty(t, state.Outcome.Failure)
			require.Len(t, state.Outcome.Injections, 2)
			assert.Equal(t, tc.wantPhase, state.Outcome.Injections[0].Phase)
			assert.Equal(t, elelem.ToolPhasePostRun, state.Outcome.Injections[1].Phase)
		})
	}
}

func TestPhaseExecutionInterruptsEveryEffectBoundary(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		phase    workflowToolPhase
		previous workflowToolPhase
		inject   bool
	}{
		{name: "pre-hook", phase: workflowToolPhasePre},
		{
			name: "handler", phase: workflowToolPhaseHandler,
			previous: workflowToolPhasePre,
		},
		{
			name: "outcome hook", phase: workflowToolPhaseOutcome,
			previous: workflowToolPhaseHandler,
		},
		{
			name: "outcome injection", phase: workflowToolPhaseOutcome,
			previous: workflowToolPhaseHandler, inject: true,
		},
		{
			name: "post-hook", phase: workflowToolPhasePost,
			previous: workflowToolPhaseOutcome,
		},
		{
			name: "post injection", phase: workflowToolPhasePost,
			previous: workflowToolPhaseOutcome, inject: true,
		},
	}
	for _, tc := range testCases {
		for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(tc.name+"/"+sentinel.Error(), func(t *testing.T) {
				t.Parallel()
				input := phaseExecutionTestInput(t)
				input.Phase, input.State.LastPhase = tc.phase, tc.previous
				input.State.Event = &workflowToolEventState{
					RawArguments: json.RawMessage(`{}`),
				}
				effects, cleanup := 0, 0
				hook := func(context.Context, *elelem.ToolEvent) error {
					if !tc.inject {
						effects++

						return sentinel
					}

					return nil
				}
				injector := func(context.Context, *elelem.ToolEvent) (
					*elelem.MessageInjection, error,
				) {
					effects++

					return nil, sentinel
				}
				tool := elelem.Tool{
					Name: workflowToolTestName, PreRun: hook,
					OnSuccess: hook, PostRun: hook,
					OnSuccessMessageInjector: injector, PostRunMessageInjector: injector,
					Handler: func(context.Context, elelem.ToolInput) (
						elelem.ToolResult, error,
					) {
						effects++

						return elelem.ToolResult{}, sentinel
					},
				}
				_, err := runWorkflowToolPhase(t.Context(), elelem.NewToolSet(tool),
					input, workflowPhaseExecutionOptions{Tool: workflowToolOptions{
						Finish: func(string) { cleanup++ },
					}})
				require.ErrorIs(t, err, sentinel)
				assert.Equal(t, 1, effects)
				assert.Equal(t, 1, cleanup)
			})
		}
	}
}

func TestPhaseExecutionRetainsOriginalDeadline(t *testing.T) {
	t.Parallel()
	input := phaseExecutionTestInput(t)
	tool := elelem.Tool{
		Name: workflowToolTestName, Timeout: time.Minute,
		Handler: func(context.Context, elelem.ToolInput) (
			elelem.ToolResult, error,
		) {
			t.Error("handler ran after its saved deadline")

			return elelem.ToolResult{}, nil
		},
	}
	state, err := runWorkflowToolPhase(t.Context(), elelem.NewToolSet(tool),
		input, workflowPhaseExecutionOptions{})
	require.NoError(t, err)
	require.False(t, state.Deadline.IsZero())
	// Model a checkpoint whose deadline elapsed while its worker was down.
	state.Deadline = state.Deadline.Add(-2 * time.Minute)
	input.Phase, input.State = workflowToolPhaseHandler, state
	tool.Timeout = time.Hour
	interrupted, err := runWorkflowToolPhase(t.Context(), elelem.NewToolSet(tool),
		input, workflowPhaseExecutionOptions{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, state.Deadline, interrupted.Deadline)
}

func TestPhaseExecutionPreservesOriginallyUnboundedTimeout(t *testing.T) {
	t.Parallel()
	input := phaseExecutionTestInput(t)
	tool := elelem.Tool{
		Name: workflowToolTestName,
		Handler: func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
			return elelem.ToolResult{Content: "completed"}, nil
		},
	}
	state, err := runWorkflowToolPhase(t.Context(), elelem.NewToolSet(tool),
		input, workflowPhaseExecutionOptions{})
	require.NoError(t, err)
	require.True(t, state.Deadline.IsZero())
	input.Phase, input.State = workflowToolPhaseHandler, state
	tool.Timeout = time.Hour
	continued, err := runWorkflowToolPhase(t.Context(), elelem.NewToolSet(tool),
		input, workflowPhaseExecutionOptions{})
	require.NoError(t, err)
	assert.True(t, continued.Deadline.IsZero())
	assert.Equal(t, "completed", continued.Outcome.Result.Content)
}

func TestPhaseExecutionPreHookFailureSkipsHandler(t *testing.T) {
	t.Parallel()
	state := runPhaseExecutionTest(t, elelem.Tool{
		Name: workflowToolTestName,
		PreRun: func(context.Context, *elelem.ToolEvent) error {
			return commerr.ErrPermissionDenied
		},
		Handler: func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
			t.Error("handler ran after pre-hook failure")

			return elelem.ToolResult{}, nil
		},
	})
	assert.True(t, state.Finished)
	assert.True(t, state.Outcome.Result.IsError)
	assert.NotEmpty(t, state.Outcome.Failure)
}

func TestPhaseExecutionKeepsMalformedCallsProtocolValid(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		missing   bool
		arguments json.RawMessage
	}{
		{name: "unknown tool", missing: true, arguments: json.RawMessage(`{}`)},
		{name: "missing handler", arguments: json.RawMessage(`{}`)},
		{name: "malformed arguments", arguments: json.RawMessage(`{`)},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := phaseExecutionTestInput(t)
			input.Tool.Call.Arguments = tc.arguments
			set := elelem.NewToolSet()
			if !tc.missing {
				set.Add(elelem.Tool{Name: workflowToolTestName})
			}

			for _, phase := range []workflowToolPhase{
				workflowToolPhasePre, workflowToolPhaseHandler, workflowToolPhaseProject,
			} {
				if input.State.Finished && phase != workflowToolPhaseProject {
					continue
				}

				input.Phase = phase
				state, err := runWorkflowToolPhase(t.Context(), set, input,
					workflowPhaseExecutionOptions{})
				require.NoError(t, err)
				input.State = state
			}
			assert.True(t, input.State.Finished)
			assert.True(t, input.State.Outcome.Result.IsError)
			assert.NotEmpty(t, input.State.Outcome.Result.Content)
			assert.Empty(t, input.State.Outcome.Failure)
		})
	}
}

func TestPhaseExecutionRejectsMissingOrDamagedDependencies(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		missingSet bool
		cancel     bool
		bindError  bool
		arguments  json.RawMessage
		wantErr    error
	}{
		{
			name: "missing tool set", missingSet: true,
			wantErr: commerr.ErrRequiredFieldNotSet,
		},
		{name: "missing event", wantErr: commerr.ErrValidationFailed},
		{
			name: "damaged event", arguments: json.RawMessage(`{`),
			wantErr: commerr.ErrValidationFailed,
		},
		{name: "cancelled context", cancel: true, wantErr: context.Canceled},
		{
			name: "binding failure", bindError: true, arguments: json.RawMessage(`{}`),
			wantErr: commerr.ErrConflict,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := phaseExecutionTestInput(t)
			input.Phase = workflowToolPhaseHandler
			input.State.LastPhase = workflowToolPhasePre
			if tc.arguments != nil {
				input.State.Event = &workflowToolEventState{RawArguments: tc.arguments}
			}

			set := elelem.NewToolSet(elelem.Tool{Name: workflowToolTestName})
			if tc.missingSet {
				set = nil
			}

			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if tc.cancel {
				cancel()
			}

			options := workflowPhaseExecutionOptions{}
			if tc.bindError {
				options.Bind = func(context.Context, *elelem.ToolEvent) error {
					return commerr.ErrConflict
				}
			}

			_, err := runWorkflowToolPhase(ctx, set, input, options)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestPhaseExecutionReplacesRemovedResult(t *testing.T) {
	t.Parallel()
	state := runPhaseExecutionTest(t, elelem.Tool{
		Name: workflowToolTestName,
		Handler: func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
			return elelem.ToolResult{Content: "original"}, nil
		},
		PostRun: func(_ context.Context, event *elelem.ToolEvent) error {
			event.Result = nil

			return nil
		},
	})
	assert.True(t, state.Outcome.Result.IsError)
	assert.NotEmpty(t, state.Outcome.Result.Content)
}
