package agent

import (
	"context"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectoryToolLifecycleFailuresReleaseTheGate(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		phase     elelem.ToolPhase
		injection bool
		panics    bool
	}{
		{name: "pre error", phase: elelem.ToolPhasePreRun},
		{name: "pre panic", phase: elelem.ToolPhasePreRun, panics: true},
		{name: "success error", phase: elelem.ToolPhaseOnSuccess},
		{name: "success panic", phase: elelem.ToolPhaseOnSuccess, panics: true},
		{name: "failure error", phase: elelem.ToolPhaseOnError},
		{name: "failure panic", phase: elelem.ToolPhaseOnError, panics: true},
		{name: "post error", phase: elelem.ToolPhasePostRun},
		{name: "post panic", phase: elelem.ToolPhasePostRun, panics: true},
		{name: "success injection", phase: elelem.ToolPhaseOnSuccess, injection: true},
		{name: "failure injection", phase: elelem.ToolPhaseOnError, injection: true},
		{name: "post injection", phase: elelem.ToolPhasePostRun, injection: true},
		{name: "post injection panic", phase: elelem.ToolPhasePostRun, injection: true, panics: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hook := func(context.Context, *elelem.ToolEvent) error {
				if tc.panics {
					panic("test hook")
				}

				return commerr.ErrPermissionDenied
			}
			injector := func(ctx context.Context, event *elelem.ToolEvent) (*elelem.MessageInjection, error) {
				return nil, hook(ctx, event)
			}
			tool := elelem.Tool{Name: toolNameChangeDirectory}
			switch {
			case tc.injection:
				tool.OnSuccessMessageInjector = injector
				tool.OnErrorMessageInjector = injector
				tool.PostRunMessageInjector = injector
			case tc.phase == elelem.ToolPhasePreRun:
				tool.PreRun = hook
			default:
				tool.OnSuccess = hook
				tool.OnError = hook
				tool.PostRun = hook
			}
			scope := &directoryScope{
				current: &directoryContext{tools: elelem.NewToolSet(tool)},
				pending: &directoryContext{prompt: "rejected candidate"},
				calls:   map[string]directoryCall{},
			}
			original := scope.context()
			event := &elelem.ToolEvent{CallID: "read", Tool: tool, Phase: elelem.ToolPhasePreRun}
			err := scope.beforeTool(context.Background(), event)
			if tc.phase != elelem.ToolPhasePreRun {
				require.NoError(t, err)
				event.Phase = tc.phase
				if tc.injection {
					_, err = scope.toolInjection(context.Background(), event)
				} else {
					err = scope.toolHook(context.Background(), event)
				}
			}
			require.Error(t, err)
			assert.Empty(t, scope.calls)
			assert.Nil(t, scope.pending)
			assert.Same(t, original, scope.context())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			t.Cleanup(cancel)
			release, err := scope.gate.acquire(ctx, true)
			require.NoError(t, err)
			release()
		})
	}
}

func TestDirectoryToolRechecksRevokedCapabilityBeforeEffects(t *testing.T) {
	t.Parallel()
	ran := false
	old := elelem.Tool{Name: toolNameMakeDirectory, Handler: func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
		ran = true

		return elelem.ToolResult{Content: "created"}, nil
	}}
	scope := &directoryScope{current: &directoryContext{tools: elelem.NewToolSet()}, calls: map[string]directoryCall{}}
	event := &elelem.ToolEvent{CallID: "revoked", Tool: old}
	require.NoError(t, scope.beforeTool(context.Background(), event))
	result, err := scope.handleTool(context.Background(), elelem.ToolInput{CallID: event.CallID})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.False(t, ran)
	event.Phase = elelem.ToolPhasePostRun
	_, err = scope.toolInjection(context.Background(), event)
	require.NoError(t, err)
	assert.Empty(t, scope.calls)
}

func TestDirectoryToolRefusesBundledHops(t *testing.T) {
	t.Parallel()
	called := false
	tool := elelem.Tool{
		Name: toolNameChangeDirectory,
		Handler: func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
			called = true

			return elelem.ToolResult{Content: "changed"}, nil
		},
	}
	scope := &directoryScope{
		current: &directoryContext{tools: elelem.NewToolSet(tool)},
		calls:   map[string]directoryCall{},
	}
	event := &elelem.ToolEvent{
		CallID: "hop", Tool: tool,
		Messages: []elelem.Message{{
			Role: elelem.RoleAssistant,
			ToolCalls: []elelem.ToolCall{
				{ID: "hop", Name: toolNameChangeDirectory},
				{ID: "read", Name: toolNameReadFile},
			},
		}},
	}
	require.NoError(t, scope.beforeTool(context.Background(), event))
	result, err := scope.handleTool(context.Background(), elelem.ToolInput{CallID: "hop"})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.False(t, called)
	scope.finishTool("hop")
}
