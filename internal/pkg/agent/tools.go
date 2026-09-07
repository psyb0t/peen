package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

const (
	toolNameListFiles     = "list_files"
	toolNameSearchText    = "search_text"
	toolNameReadFile      = "read_file"
	toolNameWriteFile     = "write_file"
	toolNameEditFile      = "edit_file"
	toolNameApplyPatch    = "apply_patch"
	toolNameMovePath      = "move_path"
	toolNameMakeDirectory = "make_directory"
	toolNameRemovePath    = "remove_path"
	toolNameRunCommand    = "run_command"
	toolNameListJobs      = "list_jobs"
	toolNameReadJobOutput = "read_job_output"
	toolNameWaitJob       = "wait_job"
	toolNameSignalJob     = "signal_job"
	toolNameUseSkill      = "use_skill"
	toolNameLaunchAgent   = "launch_agent"

	errorLocationMarker = " ["
	unknownToolError    = "tool failed"
)

// hostToolSet registers the host filesystem, command, and skill tools for
// one turn. The executor already carries the turn workspace and its
// observation ledger, so every file and job handler here is a thin typed
// adapter over it. snapshot is the turn's already-resolved harness context,
// which backs the skill tools only.
//
// launch_agent is deliberately NOT registered here: it needs a
// *launchAgentDeps that this function has no parameter for, and both of its
// callers (Runtime.runProvider and Runtime.runChildAgent) already build one.
// They add it with ToolSet.Add after calling this, via launchAgentTool.
func hostToolSet(
	executor *tools.JobExecutor,
	onPostRun elelem.MessageInjector,
	snapshot harness.Snapshot,
) *elelem.ToolSet {
	registered := readOnlyHostTools(executor, onPostRun)
	registered = append(registered, mutatingHostTools(executor, onPostRun)...)
	registered = append(registered, jobHostTools(executor, onPostRun)...)
	registered = append(registered, skillHostTools(snapshot, onPostRun)...)

	return elelem.NewToolSet(registered...)
}

func readOnlyHostTools(
	executor *tools.JobExecutor,
	onPostRun elelem.MessageInjector,
) []elelem.Tool {
	return []elelem.Tool{
		hostTool(
			toolNameListFiles,
			listFilesDescription,
			listFilesSchema,
			executor.ListFiles,
			onPostRun,
		),
		hostTool(
			toolNameSearchText,
			searchTextDescription,
			searchTextSchema,
			executor.SearchText,
			onPostRun,
		),
		hostTool(
			toolNameReadFile,
			readFileDescription,
			readFileSchema,
			executor.ReadFile,
			onPostRun,
		),
	}
}

func mutatingHostTools(
	executor *tools.JobExecutor,
	onPostRun elelem.MessageInjector,
) []elelem.Tool {
	return []elelem.Tool{
		hostTool(
			toolNameWriteFile,
			writeFileDescription,
			writeFileSchema,
			executor.WriteFile,
			onPostRun,
		),
		hostTool(
			toolNameEditFile,
			editFileDescription,
			editFileSchema,
			executor.EditFile,
			onPostRun,
		),
		applyPatchTool(executor, onPostRun),
		hostTool(
			toolNameMovePath,
			movePathDescription,
			movePathSchema,
			executor.MovePath,
			onPostRun,
		),
		hostTool(
			toolNameMakeDirectory,
			makeDirectoryDescription,
			makeDirectorySchema,
			executor.MakeDirectory,
			onPostRun,
		),
		hostTool(
			toolNameRemovePath,
			removePathDescription,
			removePathSchema,
			executor.RemovePath,
			onPostRun,
		),
		hostTool(
			toolNameRunCommand,
			runCommandDescription,
			runCommandSchema,
			executor.RunCommand,
			onPostRun,
		),
	}
}

func applyPatchTool(
	executor *tools.JobExecutor,
	onPostRun elelem.MessageInjector,
) elelem.Tool {
	return elelem.Tool{
		Name:                   toolNameApplyPatch,
		Description:            applyPatchDescription,
		ArgumentsSchema:        json.RawMessage(applyPatchSchema),
		Handler:                applyPatchToolHandler(executor.ApplyPatch),
		PostRunMessageInjector: onPostRun,
	}
}

func applyPatchToolHandler(
	run func(
		context.Context,
		tools.ApplyPatchInput,
	) (tools.ApplyPatchOutput, error),
) elelem.ToolHandler {
	return func(
		ctx context.Context,
		call elelem.ToolInput,
	) (elelem.ToolResult, error) {
		ctx = tools.ContextWithToolCallID(ctx, call.CallID)
		ctx = contextWithParentToolCallID(ctx, call.CallID)

		var input tools.ApplyPatchInput
		if err := decodeToolArguments(call.Arguments, &input); err != nil {
			return elelem.NewToolErrorResult(toolErrorMessage(err)), nil
		}

		output, err := run(ctx, input)
		if err != nil {
			if isTurnEndingError(err) {
				return elelem.ToolResult{}, ctxerrors.Wrap(
					err,
					"run apply patch tool",
				)
			}

			output.Error = toolErrorMessage(err)
		}

		encoded, marshalErr := json.Marshal(output)
		if marshalErr != nil {
			return elelem.ToolResult{}, ctxerrors.Wrap(
				marshalErr,
				"marshal apply patch result",
			)
		}

		return elelem.ToolResult{
			Content: string(encoded),
			IsError: err != nil,
		}, nil
	}
}

// jobHostTools are the process-job tools. They are split from the mutating
// file tools because they operate on supervised processes rather than the
// filesystem, and because one list of thirteen tools reads worse than two.
func jobHostTools(
	executor *tools.JobExecutor,
	onPostRun elelem.MessageInjector,
) []elelem.Tool {
	return []elelem.Tool{
		hostTool(
			toolNameListJobs,
			listJobsDescription,
			listJobsSchema,
			executor.ListJobs,
			onPostRun,
		),
		hostTool(
			toolNameReadJobOutput,
			readJobOutputDescription,
			readJobOutputSchema,
			executor.ReadJobOutput,
			onPostRun,
		),
		hostTool(
			toolNameWaitJob,
			waitJobDescription,
			waitJobSchema,
			executor.WaitJob,
			onPostRun,
		),
		hostTool(
			toolNameSignalJob,
			signalJobDescription,
			signalJobSchema,
			executor.SignalJob,
			onPostRun,
		),
	}
}

// hostTool builds one registered tool. onPostRun runs after every call and is
// how session events reach the model: it fires at a tool boundary whatever the
// agent happened to be doing.
func hostTool[Input any, Output any](
	name string,
	description string,
	schema string,
	run func(context.Context, Input) (Output, error),
	onPostRun elelem.MessageInjector,
) elelem.Tool {
	return elelem.Tool{
		Name:                   name,
		Description:            description,
		ArgumentsSchema:        json.RawMessage(schema),
		Handler:                hostToolHandler(run),
		PostRunMessageInjector: onPostRun,
	}
}

// hostToolHandler decodes typed arguments, runs the operation, and encodes the
// result. An operation failure becomes a model-visible tool error so the agent
// can recover. Only cancellation ends the turn.
func hostToolHandler[Input any, Output any](
	run func(context.Context, Input) (Output, error),
) elelem.ToolHandler {
	return func(
		ctx context.Context,
		call elelem.ToolInput,
	) (elelem.ToolResult, error) {
		// run_command records which call started a job, and the tools package
		// cannot learn the ID from the model's JSON arguments.
		ctx = tools.ContextWithToolCallID(ctx, call.CallID)
		// launch_agent records the same ID as the started run's parent tool
		// call, for internal transcript correlation.
		ctx = contextWithParentToolCallID(ctx, call.CallID)

		var input Input
		if err := decodeToolArguments(call.Arguments, &input); err != nil {
			return elelem.NewToolErrorResult(toolErrorMessage(err)), nil
		}

		output, err := run(ctx, input)
		if err != nil {
			if isTurnEndingError(err) {
				return elelem.ToolResult{}, ctxerrors.Wrap(err, "run host tool")
			}

			return elelem.NewToolErrorResult(toolErrorMessage(err)), nil
		}

		encoded, err := json.Marshal(output)
		if err != nil {
			return elelem.ToolResult{}, ctxerrors.Wrap(
				err,
				"marshal host tool result",
			)
		}

		return elelem.ToolResult{Content: string(encoded)}, nil
	}
}

func decodeToolArguments(arguments json.RawMessage, target any) error {
	if len(bytes.TrimSpace(arguments)) == 0 {
		return nil
	}

	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		return ctxerrors.Wrap(err, "decode tool arguments")
	}

	return nil
}

// isTurnEndingError separates infrastructure failure from a tool result the
// model can react to. Only the turn's own cancellation ends the turn.
func isTurnEndingError(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// toolErrorMessage renders the human part of the error chain. ctxerrors
// appends bracketed source locations after the messages, and those are
// operational detail the model must not receive.
func toolErrorMessage(err error) string {
	if err == nil {
		return unknownToolError
	}

	message := err.Error()
	if cut := strings.Index(message, errorLocationMarker); cut > 0 {
		message = message[:cut]
	}

	message = strings.TrimSpace(message)
	if message == "" {
		return unknownToolError
	}

	return message
}
