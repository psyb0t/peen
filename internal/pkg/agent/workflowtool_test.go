package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const workflowToolTestName = "exampleTool"

func workflowToolTestInput() workflowToolInput {
	return workflowToolInput{
		Round: workflowRoundInput{
			Round:     3,
			Execution: workflowExecutionInput{Context: json.RawMessage(`{}`)},
		},
		Call: elelem.ToolCall{
			ID: "exampleCall", Name: workflowToolTestName,
			Arguments: json.RawMessage(`{}`),
		},
	}
}

func TestWorkflowToolPreservesLifecycleAndMutableResult(t *testing.T) {
	t.Parallel()
	for _, isFailure := range []bool{false, true} {
		name := "success"
		if isFailure {
			name = "handler error"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()
			trace := []string{}
			hook := func(_ context.Context, event *elelem.ToolEvent) error {
				trace = append(trace, event.Phase)
				if event.Result != nil {
					event.Result.Content = "changed result"
					event.Result.Metadata = map[string]any{"example": true}
				}

				return nil
			}
			inject := func(_ context.Context, event *elelem.ToolEvent) (
				*elelem.MessageInjection, error,
			) {
				trace = append(trace, "inject:"+event.Phase)

				return &elelem.MessageInjection{
					Type: elelem.RoleUser, Content: "hook instructions",
				}, nil
			}
			tool := elelem.Tool{
				Name: workflowToolTestName, PreRun: hook,
				OnSuccess: hook, OnError: hook, PostRun: hook,
				OnSuccessMessageInjector: inject,
				OnErrorMessageInjector:   inject, PostRunMessageInjector: inject,
				Handler: func(context.Context, elelem.ToolInput) (
					elelem.ToolResult, error,
				) {
					trace = append(trace, "handler")
					if isFailure {
						return elelem.ToolResult{}, ctxerrors.Wrap(
							commerr.ErrNotFound, "example handler")
					}

					return elelem.ToolResult{Content: "original result"}, nil
				},
			}
			input := workflowToolTestInput()
			outcome, err := runWorkflowTool(t.Context(), elelem.NewToolSet(tool),
				input, workflowToolOptions{Finish: func(id string) {
					assert.Equal(t, input.Call.ID, id)
					trace = append(trace, "finish")
				}})
			require.NoError(t, err)
			phase := elelem.ToolPhaseOnSuccess
			if isFailure {
				phase = elelem.ToolPhaseOnError
			}

			assert.Equal(t, []string{
				elelem.ToolPhasePreRun, "handler", phase,
				"inject:" + phase, elelem.ToolPhasePostRun,
				"inject:" + elelem.ToolPhasePostRun, "finish",
			}, trace)
			assert.Equal(t, "changed result", outcome.Result.Content)
			assert.Equal(t, isFailure, outcome.Result.IsError)
			assert.Equal(t, map[string]any{"example": true}, outcome.Result.Metadata)
			require.Len(t, outcome.Injections, 2)
			assert.Equal(t, phase, outcome.Injections[0].Phase)
			assert.Equal(t, input.Call.ID, outcome.Injections[0].CallID)
			assert.Equal(t, input.Round.Round, outcome.Injections[0].Round)
		})
	}
}

func TestWorkflowToolPreHookFailureDoesNotRunHandler(t *testing.T) {
	t.Parallel()
	tool := elelem.Tool{
		Name: workflowToolTestName,
		PreRun: func(context.Context, *elelem.ToolEvent) error {
			return ctxerrors.Wrap(commerr.ErrConflict, "example pre-hook")
		},
		Handler: func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
			t.Error("handler ran after pre-hook failure")

			return elelem.ToolResult{}, nil
		},
	}
	outcome, err := runWorkflowTool(t.Context(), elelem.NewToolSet(tool),
		workflowToolTestInput(), workflowToolOptions{})
	require.NoError(t, err)
	assert.True(t, outcome.Result.IsError)
	assert.NotEmpty(t, outcome.Failure)
}

func TestWorkflowToolTimeoutCancelsTheHandler(t *testing.T) {
	t.Parallel()
	tool := elelem.Tool{
		Name: workflowToolTestName, Timeout: time.Millisecond,
		Handler: func(ctx context.Context, _ elelem.ToolInput) (
			elelem.ToolResult, error,
		) {
			<-ctx.Done()

			return elelem.ToolResult{}, ctxerrors.Wrap(ctx.Err(), "example handler")
		},
	}
	_, err := runWorkflowTool(t.Context(), elelem.NewToolSet(tool),
		workflowToolTestInput(), workflowToolOptions{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWorkflowToolInterruptedHandlerDoesNotStartMoreHooks(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		handlerErr error
		cancel     bool
		wantErr    error
	}{
		{
			name: "cancelled handler", handlerErr: context.Canceled,
			wantErr: context.Canceled,
		},
		{
			name: "expired handler", handlerErr: context.DeadlineExceeded,
			wantErr: context.DeadlineExceeded,
		},
		{
			name: "cancelled caller with result", cancel: true,
			wantErr: context.Canceled,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			trace := []string{}
			hook := func(_ context.Context, event *elelem.ToolEvent) error {
				trace = append(trace, event.Phase)

				return nil
			}
			inject := func(context.Context, *elelem.ToolEvent) (
				*elelem.MessageInjection, error,
			) {
				trace = append(trace, "injection")

				return &elelem.MessageInjection{Type: elelem.RoleUser}, nil
			}
			tool := elelem.Tool{
				Name: workflowToolTestName, PreRun: hook,
				OnSuccess: hook, OnError: hook, PostRun: hook,
				OnSuccessMessageInjector: inject,
				OnErrorMessageInjector:   inject,
				PostRunMessageInjector:   inject,
				Handler: func(context.Context, elelem.ToolInput) (
					elelem.ToolResult, error,
				) {
					if tc.cancel {
						cancel()
					}

					return elelem.ToolResult{Content: "partial result"}, tc.handlerErr
				},
			}
			_, err := runWorkflowTool(ctx, elelem.NewToolSet(tool),
				workflowToolTestInput(), workflowToolOptions{Finish: func(string) {
					trace = append(trace, "finish")
				}})
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, []string{elelem.ToolPhasePreRun, "finish"}, trace)
		})
	}
}

func TestWorkflowToolCancellationDuringPreHookIsInterrupted(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	tool := elelem.Tool{
		Name: workflowToolTestName,
		PreRun: func(context.Context, *elelem.ToolEvent) error {
			cancel()

			return ctxerrors.Wrap(context.Canceled, "example cancelled pre-hook")
		},
		Handler: func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
			t.Error("handler ran after cancellation")

			return elelem.ToolResult{}, nil
		},
	}
	_, err := runWorkflowTool(ctx, elelem.NewToolSet(tool),
		workflowToolTestInput(), workflowToolOptions{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestWorkflowToolInputErrorResults(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		input      workflowToolInput
		registered bool
	}{
		{name: "unknown tool", input: workflowToolTestInput()},
		{name: "missing handler", input: workflowToolTestInput(), registered: true},
		{name: "malformed arguments", input: workflowToolInput{
			Round: workflowToolTestInput().Round,
			Call: elelem.ToolCall{
				Name:      workflowToolTestName,
				Arguments: json.RawMessage(`{`),
			},
		}, registered: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			set := elelem.NewToolSet()
			if tc.registered {
				set.Add(elelem.Tool{Name: workflowToolTestName})
			}

			outcome, err := runWorkflowTool(t.Context(), set, tc.input,
				workflowToolOptions{})
			require.NoError(t, err)
			assert.True(t, outcome.Result.IsError)
			assert.NotEmpty(t, outcome.Result.Content)
		})
	}
}
