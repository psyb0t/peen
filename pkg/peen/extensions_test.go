package peen_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/pkg/peen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomToolsValidateArgumentsAndRunHooks(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		arguments  string
		wantCalled bool
	}{
		{name: "valid string", arguments: `{"value":"ok"}`, wantCalled: true},
		{name: "wrong type", arguments: `{"value":12}`},
		{name: "missing required", arguments: `{}`},
		{name: "null property", arguments: `{"value":null}`},
		{name: "unknown property", arguments: `{"value":"ok","extra":true}`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called := false
			hooked := false
			options := validOptions(t)
			options.Workspace = t.TempDir()
			driver := elelemtest.NewScriptedDriver(
				elelemtest.ToolCall("custom", "echoValue", tc.arguments),
				elelemtest.Text("done"),
			)
			options.Models[testModelReference] = peen.ModelClient{
				Client: elelem.New(driver), Model: elelem.Model{ID: testModelID},
			}
			options.Tools = []peen.Tool{{
				Name: "echoValue", Description: "Returns a test value.",
				ArgumentsSchema: json.RawMessage(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}},"additionalProperties":false}`),
				Handler: func(context.Context, peen.ToolInput) (peen.ToolResult, error) {
					called = true

					return peen.ToolResult{Content: "custom result"}, nil
				},
			}}
			options.Hooks = map[string]peen.HookHandler{
				"preToolUse": func(_ context.Context, input peen.HookInvocation) (peen.HookOutcome, error) {
					hooked = input.Tool == "echoValue"

					return peen.HookOutcome{}, nil
				},
			}
			runtime, err := peen.New(options)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
			_, err = runtime.Message(context.Background(), peen.MessageRequest{Message: "call"})
			require.NoError(t, err)
			assert.True(t, hooked)
			assert.Equal(t, tc.wantCalled, called)
		})
	}
}

func TestGoHookDeniesTool(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	options.Workspace = t.TempDir()
	called := false
	options.Tools = []peen.Tool{{
		Name: "restrictedTool", Description: "Tests a policy boundary.",
		ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, peen.ToolInput) (peen.ToolResult, error) {
			called = true

			return peen.ToolResult{}, nil
		},
	}}
	options.Hooks = map[string]peen.HookHandler{
		"preToolUse": func(context.Context, peen.HookInvocation) (peen.HookOutcome, error) {
			return peen.HookOutcome{Deny: "policy rejected operation"}, nil
		},
	}
	options.Models[testModelReference] = peen.ModelClient{
		Client: elelem.New(elelemtest.NewScriptedDriver(
			elelemtest.ToolCall("denied", "restrictedTool", `{}`),
			elelemtest.Text("refused"),
		)), Model: elelem.Model{ID: testModelID},
	}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	_, err = runtime.Message(context.Background(), peen.MessageRequest{Message: "call"})
	require.NoError(t, err)
	assert.False(t, called)
}

func TestCustomToolRejectsReservedAndDuplicateNames(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	tool := peen.Tool{
		Name: "ask", Description: "Reserved.",
		ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, peen.ToolInput) (peen.ToolResult, error) {
			return peen.ToolResult{}, nil
		},
	}
	options.Tools = []peen.Tool{tool}
	_, err := peen.New(options)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	tool.Name = "customTool"
	options.Tools = []peen.Tool{tool, tool}
	_, err = peen.New(options)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestCustomToolReplacementAndDisable(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		disabled bool
	}{
		{name: "replacement runs"},
		{name: "disabled replacement is unavailable", disabled: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := validOptions(t)
			options.Workspace = t.TempDir()
			called := false
			options.Tools = []peen.Tool{{
				Name: "readFile", Description: "Reads through the caller.",
				ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
				Handler: func(context.Context, peen.ToolInput) (peen.ToolResult, error) {
					called = true

					return peen.ToolResult{Content: "replacement result"}, nil
				},
			}}
			if tc.disabled {
				options.DisableTools = []string{"readFile"}
			}
			options.Models[testModelReference] = peen.ModelClient{
				Client: elelem.New(elelemtest.NewScriptedDriver(
					elelemtest.ToolCall("replace", "readFile", `{}`),
					elelemtest.Text("done"),
				)), Model: elelem.Model{ID: testModelID},
			}
			runtime, err := peen.New(options)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, runtime.Close(context.Background()))
			})
			_, err = runtime.Message(context.Background(),
				peen.MessageRequest{Message: "run replacement"})
			require.NoError(t, err)
			assert.Equal(t, !tc.disabled, called)
			page, err := runtime.ListMessages(context.Background(),
				peen.ListMessagesRequest{})
			require.NoError(t, err)
			var result *peen.Message
			for index := range page.Items {
				if page.Items[index].Role == peen.MessageRoleTool {
					result = &page.Items[index]
				}
			}
			require.NotNil(t, result)
			assert.Equal(t, tc.disabled, result.IsError)
			if !tc.disabled {
				assert.Equal(t, "replacement result", result.Content)
			}
		})
	}
}

type embeddingModelResolver struct {
	model peen.ModelClient
	err   error
}

func (r embeddingModelResolver) ResolveModel(string) (peen.ModelClient, error) {
	return r.model, r.err
}

func TestDynamicModelResolution(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name          string
		missingClient bool
		missingID     bool
		failure       error
		wantError     error
	}{
		{name: "resolver replaces static models"},
		{
			name: "missing client", missingClient: true,
			wantError: commerr.ErrValidationFailed,
		},
		{
			name: "missing ID", missingID: true,
			wantError: commerr.ErrValidationFailed,
		},
		{
			name: "resolver failure", failure: commerr.ErrNotFound,
			wantError: commerr.ErrNotFound,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := validOptions(t)
			options.Workspace = t.TempDir()
			options.Models = nil
			model := peen.ModelClient{
				Client: elelem.New(elelemtest.NewScriptedDriver(
					elelemtest.Text("resolved reply"),
				)), Model: elelem.Model{ID: testModelID},
			}
			if tc.missingClient {
				model.Client = nil
			}
			if tc.missingID {
				model.Model.ID = ""
			}
			options.ModelResolver = embeddingModelResolver{
				model: model, err: tc.failure,
			}
			runtime, err := peen.New(options)
			require.ErrorIs(t, err, tc.wantError)
			if tc.wantError != nil {
				require.Nil(t, runtime)

				return
			}
			t.Cleanup(func() {
				require.NoError(t, runtime.Close(context.Background()))
			})
			result, err := runtime.Message(context.Background(),
				peen.MessageRequest{Message: "resolve"})
			require.NoError(t, err)
			assert.Equal(t, "resolved reply", result.Message)
		})
	}
}
