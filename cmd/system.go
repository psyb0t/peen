package main

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/oneshot"
	"github.com/spf13/cobra"
)

// This file is project-owned. Servicepack never replaces it.

const (
	systemCommandName = "system"
	systemUse         = systemCommandName +
		" [flags] (--prompt <prompt> | <prompt>)"
	systemShort = "Run one turn as the system agent and exit"

	// systemLong keeps each help paragraph on one line, so cobra and the
	// terminal wrap it to the reader's width.
	//
	//nolint:lll // Help paragraphs are single lines by design, see above.
	systemLong = `Runs one turn in the deployment's system session and exits when the turn ends. The system agent has full access to this deployment, so PEEN_SYSTEM_AGENT must be on.

It needs a running controller and starts one when none answers, the same way "peen session" does. It never runs a controller inside this command.

The prompt is the value of --prompt, or the arguments joined by spaces, but not both. A prompt of "-" reads standard input. The exit status is the same as for "peen exec".`
)

func systemCommand() *cobra.Command {
	options := execOptions{}

	command := &cobra.Command{
		Use:           systemUse,
		Short:         systemShort,
		Long:          systemLong,
		Args:          cobra.ArbitraryArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, args []string) error {
			return runSystem(command, args, options)
		},
	}

	flags := command.Flags()
	flags.StringVar(
		&options.prompt,
		flagPrompt,
		"",
		`the message to send, or "-" to read it from stdin`,
	)
	flags.StringVar(
		&options.output,
		flagOutput,
		string(oneshot.OutputFormatText),
		"jsonl or text",
	)

	return command
}

func runSystem(
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

	system := true
	runOptions.Message.System = &system
	runOptions.System = true
	runOptions.Connect = connectRunningController

	return runOneTurn(command, runOptions)
}

// connectRunningController reaches a real controller and never falls back to
// one inside this process, which would not outlive the system agent's turn.
func connectRunningController(ctx context.Context) (oneshot.Connection, error) {
	client, err := runningControlClient(ctx)
	if err != nil {
		return oneshot.Connection{}, ctxerrors.Wrap(
			err,
			"reach the controller",
		)
	}

	return oneshot.Connection{
		Client: client,
		Close:  func(context.Context) error { return nil },
	}, nil
}
