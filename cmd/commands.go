package main

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	workerpkg "github.com/psyb0t/peen/internal/pkg/worker"
	workerrun "github.com/psyb0t/peen/internal/pkg/worker/run"
	"github.com/spf13/cobra"
)

// This file is project-owned. Servicepack never replaces it.
//
// These are the local control-client commands. Each one talks to a running
// controller over the control HTTP API. None of them opens SQLite, builds an
// agent runtime, resolves a workspace, or starts a second supervisor: when no
// controller is listening they start `peen run`, which is the supervisor.

const (
	controlReadyTimeout = 30 * time.Second

	sessionOpenUse     = "open <workspace>"
	sessionOpenShort   = "Open or resume the durable session for a workspace"
	sessionListShort   = "List the controller's durable sessions"
	sessionAttachUse   = "attach <session-id>"
	sessionAttachShort = "Stream one session's live events"
	sessionStopUse     = "stop <session-id>"
	sessionStopShort   = "Stop the session's active turn"
	controlStatShort   = "Report whether a local controller is reachable"

	workerShort = "Run one controller-launched session worker"
	workerLong  = "Internal command. A controller launches it with a launch " +
		"document on stdin. Running it by hand fails: it cannot choose a " +
		"session, an endpoint, or a workspace."
)

func commands() []*cobra.Command {
	return []*cobra.Command{
		sessionCommand(),
		controlCommand(),
		workerCommand(),
	}
}

// workerCommand runs one session worker.
//
// It is hidden because it is not a user command. The controller supplies the
// session binding, socket, credential, workspace, and profile on stdin, and
// the worker refuses to start without them.
func workerCommand() *cobra.Command {
	return &cobra.Command{
		Use:    workerpkg.WorkerCommand,
		Short:  workerShort,
		Long:   workerLong,
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := workerrun.Run(
				command.Context(),
				workerrun.Options{LaunchInput: command.InOrStdin()},
			); err != nil {
				return ctxerrors.Wrap(err, "run the session worker")
			}

			return nil
		},
	}
}

func sessionCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "session",
		Short: "Work with the controller's durable sessions",
	}

	command.AddCommand(
		sessionOpenCommand(),
		sessionListCommand(),
		sessionAttachCommand(),
		sessionStopCommand(),
	)

	return command
}

// sessionAttachCommand streams one session's live events. Attaching is read
// only: it never starts a turn.
func sessionAttachCommand() *cobra.Command {
	return &cobra.Command{
		Use:   sessionAttachUse,
		Short: sessionAttachShort,
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			sessionID, err := uuid.Parse(args[0])
			if err != nil {
				return ctxerrors.Wrap(err, "parse the session ID")
			}

			return withController(
				command.Context(),
				func(
					ctx context.Context,
					client *controlclient.Client,
				) error {
					if err := client.Attach(
						ctx,
						sessionID,
						func(event controlclient.AttachEvent) error {
							command.Printf(
								"%s\t%s\n",
								event.Type,
								string(event.Data),
							)

							return nil
						},
					); err != nil {
						return ctxerrors.Wrap(err, "attach to the session")
					}

					return nil
				},
			)
		},
	}
}

// sessionStopCommand stops the session's running work by cancelling its active
// turn. It reports when a session had nothing running, which is not an error.
func sessionStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:   sessionStopUse,
		Short: sessionStopShort,
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			sessionID, err := uuid.Parse(args[0])
			if err != nil {
				return ctxerrors.Wrap(err, "parse the session ID")
			}

			return withController(
				command.Context(),
				func(
					ctx context.Context,
					client *controlclient.Client,
				) error {
					result, err := client.CancelSession(ctx, sessionID)
					if err != nil {
						return ctxerrors.Wrap(err, "stop the session")
					}

					command.Printf(
						"%s\tcancelRequested=%t\n",
						sessionID,
						result.CancelRequested,
					)

					return nil
				},
			)
		},
	}
}

func sessionOpenCommand() *cobra.Command {
	return &cobra.Command{
		Use:   sessionOpenUse,
		Short: sessionOpenShort,
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return withController(
				command.Context(),
				func(
					ctx context.Context,
					client *controlclient.Client,
				) error {
					opened, err := client.OpenSession(ctx, args[0])
					if err != nil {
						return ctxerrors.Wrap(err, "open workspace session")
					}

					command.Printf(
						"%s\t%s\tcreated=%t\n",
						opened.Session.Id,
						opened.Session.Workspace,
						opened.Created,
					)

					return nil
				},
			)
		},
	}
}

func sessionListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: sessionListShort,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return withController(
				command.Context(),
				func(
					ctx context.Context,
					client *controlclient.Client,
				) error {
					page, err := client.ListSessions(ctx)
					if err != nil {
						return ctxerrors.Wrap(err, "list sessions")
					}

					for _, stored := range page.Items {
						command.Printf(
							"%s\t%s\t%s\n",
							stored.Id,
							stored.Workspace,
							stored.Model,
						)
					}

					return nil
				},
			)
		},
	}
}

func controlCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "control",
		Short: "Inspect the local controller",
	}

	command.AddCommand(controlStatusCommand())

	return command
}

// controlStatusCommand reports reachability without starting a controller, so
// an operator can tell "not running" from "running".
func controlStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: controlStatShort,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			client, config, err := newControlClient()
			if err != nil {
				return err
			}

			if !client.Reachable(command.Context()) {
				command.Printf(
					"controller: not running\naddress: %s\n",
					config.HTTPListenAddress,
				)

				return nil
			}

			page, err := client.ListSessions(command.Context())
			if err != nil {
				return ctxerrors.Wrap(err, "list sessions")
			}

			command.Printf(
				"controller: running\naddress: %s\nsessions: %d\n",
				config.HTTPListenAddress,
				len(page.Items),
			)

			return nil
		},
	}
}

// withController runs one operation against a reachable controller, starting
// it first when nothing is listening.
func withController(
	ctx context.Context,
	operation func(context.Context, *controlclient.Client) error,
) error {
	client, config, err := newControlClient()
	if err != nil {
		return err
	}

	if err := client.EnsureRunning(ctx, controlclient.LaunchOptions{
		StateDirectory: config.StateDirectory,
		ReadyTimeout:   controlReadyTimeout,
	}); err != nil {
		return ctxerrors.Wrap(err, "reach the local controller")
	}

	return operation(ctx, client)
}

// newControlClient reads the same deployment configuration the controller uses,
// so a command and its controller cannot disagree about the endpoint.
func newControlClient() (*controlclient.Client, peenconfig.Config, error) {
	config, err := peenconfig.Parse()
	if err != nil {
		return nil, peenconfig.Config{}, ctxerrors.Wrap(
			err,
			"load control configuration",
		)
	}

	client, err := controlclient.New(controlclient.Options{
		ListenAddress: config.HTTPListenAddress,
		Token:         config.APIToken,
	})
	if err != nil {
		return nil, peenconfig.Config{}, ctxerrors.Wrap(
			err,
			"create the control client",
		)
	}

	return client, config, nil
}
