package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	toolTestFileName = "note.txt"
	toolTestContent  = "first line\nsecond line\n"
)

func newToolTestExecutor(t *testing.T) *tools.JobExecutor {
	t.Helper()

	executor, err := tools.NewExecutor(tools.Options{Workspace: t.TempDir()})
	require.NoError(t, err)

	registry, err := tools.NewJobRegistry(uuid.New(), nil, tools.Limits{})
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, registry.Shutdown(context.Background()))
	})

	jobExecutor, err := tools.NewJobExecutor(executor, registry, uuid.New())
	require.NoError(t, err)

	return jobExecutor
}

func TestHostToolSetRegistersEveryTool(t *testing.T) {
	t.Parallel()

	set := hostToolSet(newToolTestExecutor(t), nil, harness.Snapshot{}, nil)

	wantNames := []string{
		toolNameListJobs,
		toolNameReadJobOutput,
		toolNameWaitJob,
		toolNameSignalJob,
		toolNameListFiles,
		toolNameSearchText,
		toolNameReadFile,
		toolNameWriteFile,
		toolNameEditFile,
		toolNameApplyPatch,
		toolNameMovePath,
		toolNameMakeDirectory,
		toolNameRemovePath,
		toolNameRunCommand,
		toolNameUseSkill,
	}

	definitions := set.Definitions()
	require.Len(t, definitions, len(wantNames))

	for _, name := range wantNames {
		tool, ok := set.Get(name)
		require.True(t, ok, "tool %s must be registered", name)
		assert.NotEmpty(t, tool.Description, "tool %s needs a description", name)
		assert.NotNil(t, tool.Handler, "tool %s needs a handler", name)
		assert.True(
			t,
			json.Valid(tool.ArgumentsSchema),
			"tool %s schema must be valid JSON",
			name,
		)
	}
}

func TestFileSafetyInstructionsReachTheModel(t *testing.T) {
	t.Parallel()

	assert.Contains(t, defaultSystemPrompt, "Read every existing regular file")
	assert.Contains(t, defaultSystemPrompt, "never overwrite a destination")
	assert.Contains(t, readFileDescription, toolNameApplyPatch)
	assert.Contains(t, writeFileDescription, "creation never replaces")
	assert.Contains(t, applyPatchDescription, "destinations must not exist")
	assert.Contains(t, removePathDescription, "Listing a regular file is not enough")
	assert.Contains(t, runCommandDescription, "Do not use shell redirection")
}

func TestApplyPatchToolHandlerReturnsStructuredFailure(t *testing.T) {
	t.Parallel()

	executor := newToolTestExecutor(t)
	tool, ok := hostToolSet(executor, nil, harness.Snapshot{}, nil).Get(toolNameApplyPatch)
	require.True(t, ok)

	result, err := tool.Handler(context.Background(), elelem.ToolInput{
		Name:      toolNameApplyPatch,
		CallID:    "call_1",
		Arguments: json.RawMessage(`{"patch":"invalid"}`),
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)

	output := tools.ApplyPatchOutput{}
	require.NoError(t, json.Unmarshal([]byte(result.Content), &output))
	assert.Empty(t, output.Files)
	assert.Contains(t, output.Error, tools.ErrInvalidPatch.Error())
	assert.NotContains(t, output.Error, errorLocationMarker)
}

// Every schema must reject unknown fields, otherwise a model typo silently
// becomes a default value instead of a correctable tool error.
func TestHostToolSchemasRejectUnknownFields(t *testing.T) {
	t.Parallel()

	set := hostToolSet(newToolTestExecutor(t), nil, harness.Snapshot{}, nil)

	for _, tool := range set.Definitions() {
		schema := map[string]any{}
		require.NoError(t, json.Unmarshal(tool.ArgumentsSchema, &schema))

		assert.Equal(
			t,
			false,
			schema["additionalProperties"],
			"tool %s must reject unknown fields",
			tool.Name,
		)
	}
}

func TestHostToolHandlerReturnsEncodedResult(t *testing.T) {
	t.Parallel()

	executor := newToolTestExecutor(t)
	path := filepath.Join(executor.Workspace(), toolTestFileName)
	require.NoError(t, os.WriteFile(path, []byte(toolTestContent), 0o600))

	tool, ok := hostToolSet(executor, nil, harness.Snapshot{}, nil).Get(toolNameReadFile)
	require.True(t, ok)

	result, err := tool.Handler(context.Background(), elelem.ToolInput{
		Name:      toolNameReadFile,
		CallID:    "call_1",
		Arguments: json.RawMessage(`{"path":"` + toolTestFileName + `"}`),
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)

	output := tools.ReadFileOutput{}
	require.NoError(t, json.Unmarshal([]byte(result.Content), &output))
	assert.Equal(t, toolTestContent, output.Content)
	assert.Equal(t, 2, output.TotalLines)
	assert.NotEmpty(t, output.SHA256)
}

// A tool that fails must hand the model a readable error and let the turn
// continue, so the agent can correct itself instead of the request dying.
func TestHostToolHandlerReportsFailuresToTheModel(t *testing.T) {
	t.Parallel()

	executor := newToolTestExecutor(t)

	testCases := []struct {
		name      string
		tool      string
		arguments string
	}{
		{
			name:      "missing file",
			tool:      toolNameReadFile,
			arguments: `{"path":"absent.txt"}`,
		},
		{
			name:      "unknown argument field",
			tool:      toolNameReadFile,
			arguments: `{"path":"a.txt","nope":1}`,
		},
		{
			name:      "malformed arguments",
			tool:      toolNameReadFile,
			arguments: `{"path":`,
		},
		{
			name:      "unobserved edit target",
			tool:      toolNameEditFile,
			arguments: `{"path":"a.txt","edits":[{"old":"x","new":"y"}]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tool, ok := hostToolSet(executor, nil, harness.Snapshot{}, nil).Get(tc.tool)
			require.True(t, ok)

			result, err := tool.Handler(context.Background(), elelem.ToolInput{
				Name:      tc.tool,
				CallID:    "call_1",
				Arguments: json.RawMessage(tc.arguments),
			})

			require.NoError(t, err, "a tool failure must not end the turn")
			assert.True(t, result.IsError)
			assert.NotEmpty(t, result.Content)
			assert.NotContains(
				t,
				result.Content,
				errorLocationMarker,
				"model-visible errors must not carry source locations",
			)
		})
	}
}

// Cancellation is infrastructure failure, not a tool result: it has to end the
// turn rather than be handed back for the model to retry.
func TestHostToolHandlerEndsTurnOnCancellation(t *testing.T) {
	t.Parallel()

	executor := newToolTestExecutor(t)

	tool, ok := hostToolSet(executor, nil, harness.Snapshot{}, nil).Get(toolNameRunCommand)
	require.True(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tool.Handler(ctx, elelem.ToolInput{
		Name:      toolNameRunCommand,
		CallID:    "call_1",
		Arguments: json.RawMessage(`{"command":"true"}`),
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestToolErrorMessageStripsSourceLocations(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil error",
			err:  nil,
			want: unknownToolError,
		},
		{
			name: "wrapped sentinel",
			err: ctxerrors.Wrap(
				tools.ErrNotObserved,
				"read the path before changing it",
			),
			want: "read the path before changing it: " +
				tools.ErrNotObserved.Error(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, toolErrorMessage(tc.err))
		})
	}
}
