package main

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/config/envtemplate"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/nondumpable"
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
	sessionAttachLong  = "Streams one session's live events and starts no " +
		"turn. It reconnects automatically after an unexpected drop and " +
		"catches up from the session feed, so no frame is missed or " +
		"repeated. --cursor replays from that sequence before joining live."
	sessionAttachCursorFlag  = "cursor"
	sessionAttachCursorUsage = "replay the session feed from this " +
		"sequence before continuing live"
	sessionStopUse   = "stop <session-id>"
	sessionStopShort = "Stop the session's active turn"
	controlStatShort = "Report whether a local controller is reachable"

	configShort     = "Prepare Peen's configuration"
	configInitShort = "Write a new commented .env file"
	configInitLong  = "Writes the commented .env template to --env-file with " +
		"the given settings, mode 0600. It refuses a file that already " +
		"exists and leaves it unchanged. It reads no other configuration, " +
		"so it runs before any .env exists."

	flagEnvFile           = "env-file"
	flagConfigDir         = "config-dir"
	flagStateDir          = "state-dir"
	flagWorkspaceRoot     = "workspace-root"
	flagDefaultAgentSpace = "default-agent-space"
	flagWorkerImage       = "worker-image"
	flagHTTPListenAddress = "http-listen-address"
	flagHostUsername      = "host-username"
	flagHostHome          = "host-home"
	flagControlSpace      = "control-space"
	flagImage             = "image"
	flagContainerName     = "container-name"

	workerShort = "Run one controller-launched session worker"
	workerLong  = "Internal command. A controller launches it with a launch " +
		"document on stdin. Running it by hand fails: it cannot choose a " +
		"session, an endpoint, or a workspace."
)

func commands() []*cobra.Command {
	return []*cobra.Command{
		sessionCommand(),
		execCommand(),
		controlCommand(),
		configCommand(),
		workerCommand(workerCommandDependencies{}),
	}
}

func configCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: configShort,
	}

	command.AddCommand(configInitCommand())

	return command
}

// configInitCommand writes a new .env from the template.
//
// An installer runs it through the image or the host binary before any .env
// exists, so it must never parse PEEN_ configuration or start a service. It
// reads only its own flags.
func configInitCommand() *cobra.Command {
	var (
		envFile string
		values  envtemplate.Values
	)

	command := &cobra.Command{
		Use:   "init",
		Short: configInitShort,
		Long:  configInitLong,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := envtemplate.WriteNew(
				command.Context(),
				envFile,
				values,
			); err != nil {
				return ctxerrors.Wrap(err, "write the env file")
			}

			command.Printf("wrote %s\n", envFile)

			return nil
		},
	}

	flags := command.Flags()
	flags.StringVar(&envFile, flagEnvFile, "", "absolute path of the new .env")

	for _, flag := range configInitValueFlags(&values) {
		flags.StringVar(flag.target, flag.name, "", flag.usage)
	}

	// MarkFlagRequired fails only for an undefined flag, and this one is
	// defined above.
	_ = command.MarkFlagRequired(flagEnvFile)

	return command
}

// configInitValueFlag binds one `config init` flag to the setting it writes.
type configInitValueFlag struct {
	name   string
	target *string
	usage  string
}

func configInitValueFlags(values *envtemplate.Values) []configInitValueFlag {
	return []configInitValueFlag{
		{
			flagConfigDir,
			&values.ConfigDirectory,
			"PEEN_CONFIG_DIR, an absolute path",
		},
		{
			flagStateDir,
			&values.StateDirectory,
			"PEEN_STATE_DIR, an absolute path",
		},
		{
			flagWorkspaceRoot,
			&values.WorkspaceRoot,
			"PEEN_WORKSPACE_ROOT, an absolute path",
		},
		{
			flagDefaultAgentSpace,
			&values.DefaultAgentSpace,
			"PEEN_DEFAULT_AGENT_SPACE, control or docker:<profile>",
		},
		{flagWorkerImage, &values.WorkerImage, "PEEN_WORKER_IMAGE"},
		{
			flagHTTPListenAddress,
			&values.HTTPListenAddress,
			"PEEN_HTTP_LISTEN_ADDRESS",
		},
		{flagHostUsername, &values.HostUsername, "PEEN_HOST_USERNAME"},
		{
			flagHostHome,
			&values.HostHome,
			"PEEN_HOST_HOME, an absolute path",
		},
		{
			flagControlSpace,
			&values.ControlSpace,
			"PEEN_CONTROL_SPACE, host or docker",
		},
		{flagImage, &values.Image, "PEEN_IMAGE"},
		{flagContainerName, &values.ContainerName, "PEEN_CONTAINER_NAME"},
	}
}

// workerCommandDependencies are the worker command's seams. A nil field takes
// its production default.
type workerCommandDependencies struct {
	enableNonDumpable func(context.Context) error
}

// workerCommand runs one session worker.
//
// It is hidden because it is not a user command. The controller supplies the
// session binding, socket, credential, workspace, and agent space on stdin, and
// the worker refuses to start without them.
func workerCommand(dependencies workerCommandDependencies) *cobra.Command {
	if dependencies.enableNonDumpable == nil {
		dependencies.enableNonDumpable = nondumpable.Enable
	}

	return &cobra.Command{
		Use:    workerpkg.WorkerCommand,
		Short:  workerShort,
		Long:   workerLong,
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			// First, before the launch document with the worker credential
			// is read: the agent's commands run as this user and must not
			// read the provider keys from this process's /proc files.
			if err := dependencies.enableNonDumpable(
				command.Context(),
			); err != nil {
				return ctxerrors.Wrap(err, "protect the worker process")
			}

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

// sessionAttachCommand streams one session's live events and starts no turn.
// It reconnects after an unexpected drop and catches up from the session
// feed, so a client left running never misses or repeats a frame.
func sessionAttachCommand() *cobra.Command {
	var cursor int64

	command := &cobra.Command{
		Use:   sessionAttachUse,
		Short: sessionAttachShort,
		Long:  sessionAttachLong,
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			sessionID, err := uuid.Parse(args[0])
			if err != nil {
				return ctxerrors.Wrap(err, "parse the session ID")
			}

			cursorArg, err := sessionAttachCursorArg(command, cursor)
			if err != nil {
				return err
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
						cursorArg,
						func(event controlclient.AttachEvent) error {
							command.Printf(
								"%s\t%s\t%s\n",
								sessionAttachSequenceLabel(event.Sequence),
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

	command.Flags().Int64Var(
		&cursor,
		sessionAttachCursorFlag,
		0,
		sessionAttachCursorUsage,
	)

	return command
}

// sessionAttachCursorArg resolves the --cursor flag into the optional replay
// start Attach takes. The flag has no meaningful zero value of its own, so
// whether it was set, not what it defaults to, decides whether Attach replays.
func sessionAttachCursorArg(
	command *cobra.Command,
	cursor int64,
) (*int64, error) {
	if !command.Flags().Changed(sessionAttachCursorFlag) {
		//nolint:nilnil // No cursor means start live at the current head.
		return nil, nil
	}

	if cursor < 0 {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"--cursor must not be negative",
		)
	}

	return &cursor, nil
}

// sessionAttachSequenceLabel is the first column of one printed frame: its
// feed sequence, or "-" for an unsequenced live-only acknowledgement such as
// message.completed or message.failed.
func sessionAttachSequenceLabel(sequence *int64) string {
	if sequence == nil {
		return "-"
	}

	return strconv.FormatInt(*sequence, 10)
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
					opened, err := client.OpenSession(
						ctx,
						api.OpenSessionRequest{Workspace: args[0]},
					)
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
