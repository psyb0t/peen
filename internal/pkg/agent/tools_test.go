package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/naming"
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

	set := hostToolSet(newToolTestExecutor(t), nil, harness.Snapshot{}, nil, nil)

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

func TestToolNamesAreCamelCase(t *testing.T) {
	t.Parallel()

	set := hostToolSet(newToolTestExecutor(t), nil, harness.Snapshot{}, nil, nil)

	definitions := set.Definitions()

	names := make([]string, 0, len(definitions)+5)
	names = append(
		names,
		toolNameLaunchAgent,
		toolNameSendMessage,
		toolNameWaitMessages,
		toolNameStopAgent,
		toolNameAsk,
		toolNameSearchExecutions,
		toolNameReadExecution,
		toolNameResumeExecution,
		toolNameResolveExecutionActivity,
	)

	for _, tool := range definitions {
		names = append(names, tool.Name)
	}

	for _, name := range names {
		assert.True(t, naming.IsValid(name), "tool name %q", name)
	}
}

func TestRestrictToolSet(t *testing.T) {
	t.Parallel()

	base := hostToolSet(newToolTestExecutor(t), nil, harness.Snapshot{}, nil, nil)
	restricted, err := restrictToolSet(
		base,
		[]string{toolNameListFiles, toolNameReadFile},
	)
	require.NoError(t, err)

	_, found := restricted.Get(toolNameListFiles)
	assert.True(t, found)
	_, found = restricted.Get(toolNameReadFile)
	assert.True(t, found)
	_, found = restricted.Get(toolNameApplyPatch)
	assert.False(t, found)

	_, err = restrictToolSet(base, []string{"not_a_tool"})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
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
	tool, ok := hostToolSet(executor, nil, harness.Snapshot{}, nil, nil).Get(toolNameApplyPatch)
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

	set := hostToolSet(newToolTestExecutor(t), nil, harness.Snapshot{}, nil, nil)

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

// runCommand and waitJob take a Go duration string as timeout. The retired
// integer timeoutSeconds field must fail as an unknown field.
func TestWaitTimeoutToolsTakeADurationString(t *testing.T) {
	t.Parallel()

	executor := newToolTestExecutor(t)
	set := hostToolSet(executor, nil, harness.Snapshot{}, nil, nil)

	started, err := executor.RunCommand(
		context.Background(),
		tools.RunCommandInput{
			Purpose:    "wait target",
			Command:    "echo done-output; exit 3",
			Background: true,
		},
	)
	require.NoError(t, err)

	jobID := started.JobID.String()

	testCases := []struct {
		name         string
		tool         string
		arguments    string
		wantError    bool
		wantContains []string
	}{
		{
			name:      "runCommand timeout string",
			tool:      toolNameRunCommand,
			arguments: `{"command":"echo hello","purpose":"check","timeout":"1m30s"}`,
			wantContains: []string{
				`"purpose":"check"`,
				`"background":false`,
				`"state":"exited"`,
				`"exitCode":0`,
				`"stdout":"hello"`,
			},
		},
		{
			name:      "runCommand retired timeoutSeconds",
			tool:      toolNameRunCommand,
			arguments: `{"command":"true","purpose":"check","timeoutSeconds":5}`,
			wantError: true,
		},
		{
			name:      "runCommand integer timeout",
			tool:      toolNameRunCommand,
			arguments: `{"command":"true","purpose":"check","timeout":5}`,
			wantError: true,
		},
		{
			name:      "waitJob timeout string",
			tool:      toolNameWaitJob,
			arguments: `{"jobId":"` + jobID + `","timeout":"5s"}`,
			wantContains: []string{
				`"found":true`,
				`"running":false`,
				`"state":"exited"`,
				`"exitCode":3`,
				`"stdout":"done-output"`,
			},
		},
		{
			name:      "waitJob retired timeoutSeconds",
			tool:      toolNameWaitJob,
			arguments: `{"jobId":"` + jobID + `","timeoutSeconds":5}`,
			wantError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tool, ok := set.Get(tc.tool)
			require.True(t, ok)

			result, err := tool.Handler(context.Background(), elelem.ToolInput{
				Name:      tc.tool,
				CallID:    "call_1",
				Arguments: json.RawMessage(tc.arguments),
			})
			require.NoError(t, err)
			assert.Equal(t, tc.wantError, result.IsError, result.Content)

			for _, want := range tc.wantContains {
				assert.Contains(t, result.Content, want)
			}
		})
	}
}

func TestWaitTimeoutToolSchemasDeclareTimeoutAsAString(t *testing.T) {
	t.Parallel()

	set := hostToolSet(newToolTestExecutor(t), nil, harness.Snapshot{}, nil, nil)

	for _, name := range []string{toolNameRunCommand, toolNameWaitJob} {
		tool, ok := set.Get(name)
		require.True(t, ok)

		schema := struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}{}
		require.NoError(t, json.Unmarshal(tool.ArgumentsSchema, &schema))

		assert.Equal(t, "string", schema.Properties["timeout"].Type, name)
		assert.NotContains(t, schema.Properties, "timeoutSeconds", name)
	}

	assert.Contains(t, runCommandDescription, "timeout is a Go duration string")
	assert.NotContains(t, runCommandDescription, "timeoutSeconds")
}

// runCommand and waitJob wait as long as the call asks, so PEEN_TOOL_TIMEOUT
// must not cut them off, and the model has to learn the default wait and that
// it may ask for a longer one. Every other tool keeps the configured limit.
func TestWaitingToolsOutliveTheToolTimeout(t *testing.T) {
	t.Parallel()

	set := hostToolSet(newToolTestExecutor(t), nil, harness.Snapshot{}, nil, nil)
	defaultWait := tools.DefaultLimits().CommandTimeout.String()

	for _, name := range []string{toolNameRunCommand, toolNameWaitJob} {
		tool, ok := set.Get(name)
		require.True(t, ok)
		assert.Equal(t, waitingToolTimeout, tool.Timeout, name)
		assert.Contains(t, tool.Description, "empty waits "+defaultWait, name)
		assert.Contains(t, tool.Description, "upper limit", name)
	}

	for _, name := range []string{toolNameReadFile, toolNameListJobs} {
		tool, ok := set.Get(name)
		require.True(t, ok)
		assert.Zero(t, tool.Timeout, name)
	}
}

// Only PEEN_ASK_TIMEOUT decides how long ask waits, so the model is not
// offered a timeout argument.
func TestAskSchemaOffersNoTimeout(t *testing.T) {
	t.Parallel()

	schema := struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}{}
	require.NoError(t, json.Unmarshal([]byte(askSchema), &schema))
	assert.Contains(t, schema.Properties, "question")
	assert.NotContains(t, schema.Properties, "timeout")
}

func TestHostToolHandlerReturnsEncodedResult(t *testing.T) {
	t.Parallel()

	executor := newToolTestExecutor(t)
	path := filepath.Join(executor.Workspace(), toolTestFileName)
	require.NoError(t, os.WriteFile(path, []byte(toolTestContent), 0o600))

	tool, ok := hostToolSet(executor, nil, harness.Snapshot{}, nil, nil).Get(toolNameReadFile)
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

			tool, ok := hostToolSet(executor, nil, harness.Snapshot{}, nil, nil).Get(tc.tool)
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

	tool, ok := hostToolSet(executor, nil, harness.Snapshot{}, nil, nil).Get(toolNameRunCommand)
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
