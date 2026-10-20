package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/psyb0t/peen/internal/pkg/oneshot"
	"github.com/psyb0t/slogging/handlers"
	"github.com/psyb0t/slogging/slogconf"
	"github.com/spf13/cobra"
)

// This file is project-owned. Servicepack never replaces it.

// execLong keeps each help paragraph on one line, so cobra and the terminal
// wrap it to the reader's width.
//
//nolint:lll // Help paragraphs are single lines by design, see above.
const execLong = `Runs one turn for the session of a workspace, the current directory by default, and exits when the turn ends.

It uses the controller already running at PEEN_HTTP_LISTEN_ADDRESS and leaves it running. With none running it starts a controller inside this process for the turn and stops it afterwards. That controller serves its API only inside the process: it opens no port and no metrics listener.

The prompt is the value of --prompt, or the arguments joined by spaces, but not both. A prompt of "-" reads standard input. Every PEEN_ setting comes from the environment, as for "peen run".

By default standard output carries every frame of the session exactly as the WebSocket sends it, one JSON object per line: the model's thinking and reply deltas, tool calls and results, child agent runs, and the turn's end. --output text writes the reply to standard output and the rest to standard error. Logs go to standard error at warn level unless LOG_LEVEL says otherwise.

The first interrupt abandons startup or cancels the running turn and waits for confirmation. A second stops streaming and shuts down an in-process controller; a third exits immediately. The exit status is 0 when the turn completed, 1 when it failed, and 130 when it was cancelled.`

const (
	execUse   = "exec [flags] (--prompt <prompt> | <prompt>)"
	execShort = "Run one turn in a workspace and exit"

	flagPrompt           = "prompt"
	flagModel            = "model"
	flagReasoningEffort  = "reasoning-effort"
	flagSystemPrompt     = "system-prompt"
	flagSystemPromptMode = "system-prompt-mode"
	flagAgentSpace       = "agent-space"
	flagWorkspace        = "workspace"
	flagOutput           = "output"

	// promptFromStdin is the one argument that reads the prompt from
	// standard input.
	promptFromStdin = "-"

	// exitTurnFailed and exitTurnCancelled follow the shell conventions for a
	// failure and for an interrupt (128 + SIGINT).
	exitTurnFailed    = 1
	exitTurnCancelled = 130

	// Signals escalate from interrupting the turn to abandoning the run.
	signalInterrupt = 1
	signalAbandon   = 2

	// execDefaultLogLevel keeps the controller's lifecycle records out of an
	// interactive run. LOG_LEVEL still overrides it.
	execDefaultLogLevel = "warn"
)

// exitCodeError carries a process exit status out of a command. main exits
// with it and does not log it as a failure.
type exitCodeError struct {
	code int
}

func (e exitCodeError) Error() string {
	return "exit status " + strconv.Itoa(e.code)
}

// execOptions are the exec command's flags.
type execOptions struct {
	prompt           string
	model            string
	reasoningEffort  string
	systemPrompt     string
	systemPromptMode string
	agentSpace       string
	workspace        string
	output           string
}

func execCommand() *cobra.Command {
	options := execOptions{}

	command := &cobra.Command{
		Use:   execUse,
		Short: execShort,
		Long:  execLong,
		Args:  cobra.ArbitraryArgs,
		// A failed turn is an exit status, not a usage mistake, and main
		// logs any other error itself.
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, args []string) error {
			return runExec(command, args, options)
		},
	}

	bindExecFlags(command, &options)

	return command
}

// bindExecFlags registers the exec flags on command, writing into options.
func bindExecFlags(command *cobra.Command, options *execOptions) {
	flags := command.Flags()
	flags.StringVar(
		&options.prompt,
		flagPrompt,
		"",
		`the message to send, or "-" to read it from stdin`,
	)
	flags.StringVar(
		&options.model,
		flagModel,
		"",
		"provider/model for this turn, one of GET /api/v1/models",
	)
	flags.StringVar(
		&options.reasoningEffort,
		flagReasoningEffort,
		"",
		"minimal, low, medium, high, xhigh, or max",
	)
	flags.StringVar(
		&options.systemPrompt,
		flagSystemPrompt,
		"",
		"instructions for this turn",
	)
	flags.StringVar(
		&options.systemPromptMode,
		flagSystemPromptMode,
		string(wsapi.MessageSystemPromptModeAppend),
		"append to the default system prompt, or replace it",
	)
	flags.StringVar(
		&options.agentSpace,
		flagAgentSpace,
		"",
		"control, docker, or docker:<profile>; moves an existing session",
	)
	flags.StringVar(
		&options.workspace,
		flagWorkspace,
		"",
		"workspace directory, default the current directory",
	)
	flags.StringVar(
		&options.output,
		flagOutput,
		string(oneshot.OutputFormatJSONL),
		"jsonl or text",
	)
}

func runExec(
	command *cobra.Command,
	args []string,
	options execOptions,
) error {
	if err := routeLogsToStderr(); err != nil {
		return err
	}

	runOptions, err := execRunOptions(command, args, options)
	if err != nil {
		return err
	}

	ctx, interrupt, stopSignals := execSignals(command.Context())
	defer stopSignals()

	runOptions.Interrupt = interrupt

	result, err := oneshot.Run(ctx, runOptions)
	if errors.Is(err, oneshot.ErrQuestionUnanswerable) {
		command.PrintErrln(oneshot.ErrQuestionUnanswerable.Error())
	}

	return execResult(result, err)
}

func execResult(result controlclient.TurnResult, err error) error {
	if errors.Is(err, context.Canceled) {
		return exitCodeError{code: exitTurnCancelled}
	}

	if err != nil && !errors.Is(err, oneshot.ErrQuestionUnanswerable) {
		return ctxerrors.Wrap(err, "run the turn")
	}

	return execExitCode(result)
}

func execExitCode(result controlclient.TurnResult) error {
	switch result.Outcome {
	case controlclient.TurnOutcomeCompleted:
		return nil
	case controlclient.TurnOutcomeCancelled:
		return exitCodeError{code: exitTurnCancelled}
	case controlclient.TurnOutcomeFailed:
		return exitCodeError{code: exitTurnFailed}
	}

	return exitCodeError{code: exitTurnFailed}
}

// execRunOptions turns the arguments and flags into the run's options.
func execRunOptions(
	command *cobra.Command,
	args []string,
	options execOptions,
) (oneshot.Options, error) {
	output, err := oneshot.ParseOutputFormat(options.output)
	if err != nil {
		return oneshot.Options{}, ctxerrors.Wrap(err, "read --output")
	}

	args, err = execPromptArguments(args, options.prompt)
	if err != nil {
		return oneshot.Options{}, err
	}

	isPromptFromStdin := len(args) == 1 && args[0] == promptFromStdin

	prompt, err := execPrompt(command.InOrStdin(), args, isPromptFromStdin)
	if err != nil {
		return oneshot.Options{}, err
	}

	message, err := execMessage(prompt, options)
	if err != nil {
		return oneshot.Options{}, err
	}

	workspace, err := execWorkspace(options.workspace)
	if err != nil {
		return oneshot.Options{}, err
	}

	runOptions := oneshot.Options{
		Workspace:  workspace,
		AgentSpace: options.agentSpace,
		Message:    message,
		Output:     output,
		Stdout:     command.OutOrStdout(),
		Stderr:     command.ErrOrStderr(),
	}

	// Only a person at a terminal can answer a question, and only when the
	// prompt did not already consume standard input.
	if !isPromptFromStdin && isTerminal(os.Stdin) {
		runOptions.Answers = os.Stdin
	}

	return runOptions, nil
}

// execPromptArguments takes the prompt from --prompt or from the positional
// arguments, and refuses both at once so a stray word is never appended.
func execPromptArguments(args []string, prompt string) ([]string, error) {
	if prompt == "" {
		return args, nil
	}

	if len(args) > 0 {
		return nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"give the prompt with --%s or as arguments, not both",
			flagPrompt,
		)
	}

	return []string{prompt}, nil
}

func execPrompt(
	stdin io.Reader,
	args []string,
	isPromptFromStdin bool,
) (string, error) {
	prompt := strings.Join(args, " ")

	if isPromptFromStdin {
		read, err := io.ReadAll(stdin)
		if err != nil {
			return "", ctxerrors.Wrap(err, "read the prompt from stdin")
		}

		prompt = string(read)
	}

	if strings.TrimSpace(prompt) == "" {
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"the prompt is empty",
		)
	}

	return prompt, nil
}

// execMessage builds the turn's message.send data. The controller validates
// the model and reasoning effort; these checks only catch a bad flag before a
// controller starts.
func execMessage(
	prompt string,
	options execOptions,
) (wsapi.MessageSendData, error) {
	message := wsapi.MessageSendData{Message: prompt}

	if options.model != "" {
		message.Model = &options.model
	}

	if options.reasoningEffort != "" {
		effort := wsapi.MessageSendReasoningEffort(options.reasoningEffort)
		message.ReasoningEffort = &effort
	}

	if options.systemPrompt != "" {
		message.SystemPrompt = &wsapi.MessageSystemPrompt{
			Content: options.systemPrompt,
			Mode:    wsapi.MessageSystemPromptMode(options.systemPromptMode),
		}
	}

	if err := message.Validate(); err != nil {
		return wsapi.MessageSendData{}, ctxerrors.Wrap(
			err,
			"check the turn flags",
		)
	}

	return message, nil
}

// execWorkspace resolves the workspace to an absolute path. The controller
// resolves symlinks and checks it against the workspace roots.
func execWorkspace(workspace string) (string, error) {
	if workspace == "" {
		current, err := os.Getwd()
		if err != nil {
			return "", ctxerrors.Wrap(err, "read the current directory")
		}

		return current, nil
	}

	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve --workspace")
	}

	return absolute, nil
}

// execSignals maps SIGINT, SIGTERM, and SIGHUP onto the run.
//
// The first one is an interrupt: before the turn it abandons the run, during
// the turn it cancels the turn and waits for the controller to confirm. The
// second cancels ctx, which stops streaming, cancels the turn, and stops an
// in-process controller cleanly. The third exits at once, for a shutdown that
// hangs. SIGPIPE is ignored, so a closed output pipe fails the next write and
// the run shuts down cleanly instead of the process dying mid-turn.
func execSignals(
	parent context.Context,
) (context.Context, <-chan struct{}, func()) {
	ctx, cancel := context.WithCancel(parent)
	interrupt := make(chan struct{}, 1)

	signal.Ignore(syscall.SIGPIPE)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	done := make(chan struct{})

	go func() {
		received := 0

		for {
			select {
			case <-done:
				return
			case <-signals:
			}

			received++

			switch received {
			case signalInterrupt:
				interrupt <- struct{}{}
			case signalAbandon:
				cancel()
			default:
				os.Exit(exitTurnCancelled)
			}
		}
	}()

	return ctx, interrupt, func() {
		signal.Stop(signals)
		close(done)
		cancel()
	}
}

// routeLogsToStderr sends every log record to stderr, because stdout carries
// the turn's frames. It reads the logging library's own LOG_LEVEL, LOG_FORMAT,
// and LOG_ADD_SOURCE, with warn as the default level, and keeps the audit
// sink the controller adds later.
func routeLogsToStderr() error {
	level := slog.LevelWarn

	levelName := os.Getenv(slogconf.EnvVarNameLevel)
	if levelName == "" {
		levelName = execDefaultLogLevel
	}

	if err := level.UnmarshalText([]byte(levelName)); err != nil {
		return ctxerrors.Wrapf(err, "read %s", slogconf.EnvVarNameLevel)
	}

	addSource := false

	if raw := os.Getenv(slogconf.EnvVarNameAddSource); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return ctxerrors.Wrapf(
				err,
				"read %s",
				slogconf.EnvVarNameAddSource,
			)
		}

		addSource = parsed
	}

	handler, err := handlers.New(
		handlers.Options{
			Format: handlers.Format(
				strings.ToLower(os.Getenv(slogconf.EnvVarNameFormat)),
			),
			Level:     level,
			AddSource: addSource,
		},
		handlers.Stdout(os.Stderr),
		handlers.Stderr(os.Stderr),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "build the stderr log handler")
	}

	slogconf.SetOutput(handler)

	return nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}
