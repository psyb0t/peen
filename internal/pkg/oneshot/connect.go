package oneshot

import (
	"context"
	"errors"
	"runtime/debug"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/app"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/control"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	"github.com/psyb0t/peen/internal/pkg/memlistener"
)

const (
	// inProcessAddress is the host the client writes into request URLs for
	// an in-process controller. The in-memory dial ignores it, so it names
	// nothing on the network.
	inProcessAddress = "peen.in-process:80"

	// inProcessReadyTimeout bounds controller startup, which includes
	// provider model discovery and the Docker daemon probe.
	inProcessReadyTimeout = 2 * time.Minute

	inProcessReadyPollInterval = 50 * time.Millisecond

	// inProcessProbeTimeout bounds one readiness probe. The in-memory API
	// answers in microseconds once it serves.
	inProcessProbeTimeout = time.Second
)

// Connection is the controller a run talks to and how to let it go.
type Connection struct {
	Client *controlclient.Client

	// Close stops the controller when the run started it and does nothing
	// when the run borrowed a running one.
	Close func(context.Context) error
}

// ConnectFunc reaches the controller a run uses.
type ConnectFunc func(ctx context.Context) (Connection, error)

// Connect uses the controller already answering at PEEN_HTTP_LISTEN_ADDRESS,
// and leaves it running afterwards. With none answering it starts a controller
// inside this process that serves its API on an in-memory listener, so it
// opens no port and nothing outside the process can reach it, and Close stops
// it.
func Connect(ctx context.Context) (Connection, error) {
	config, err := peenconfig.Parse()
	if err != nil {
		return Connection{}, ctxerrors.Wrap(err, "load control configuration")
	}

	shared, err := controlclient.New(controlclient.Options{
		ListenAddress: config.HTTPListenAddress,
		Token:         config.APIToken,
	})
	if err != nil {
		return Connection{}, ctxerrors.Wrap(err, "create the control client")
	}

	if shared.Reachable(ctx) {
		ctxscope.GetLogger(ctx).Info(
			"using the running controller",
			"listen_address", config.HTTPListenAddress,
		)

		return Connection{Client: shared, Close: closeNothing}, nil
	}

	return startInProcess(ctx, app.GetInstance(), config.APIToken)
}

func closeNothing(context.Context) error {
	return nil
}

// runnable is the controller lifecycle startInProcess drives.
type runnable interface {
	Run(ctx context.Context) error
}

// startInProcess runs the controller in this process and waits until its API
// answers over the in-memory listener.
func startInProcess(
	ctx context.Context,
	controller runnable,
	token string,
) (Connection, error) {
	listener := memlistener.New()
	endpoint := control.SharedInProcessEndpoint()
	endpoint.Set(listener)

	client, err := controlclient.New(controlclient.Options{
		ListenAddress: inProcessAddress,
		Token:         token,
		Dial:          listener.DialContext,
	})
	if err != nil {
		endpoint.Clear()

		return Connection{}, ctxerrors.Wrap(err, "create the control client")
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	run := &controllerRun{done: make(chan struct{})}

	go func() {
		defer close(run.done)

		run.err = runController(runCtx, controller)
	}()

	// Every path waits for the controller to return, so SQLite is closed and
	// every worker stopped before the command exits.
	release := func() error {
		cancelRun()
		<-run.done
		endpoint.Clear()

		return listener.Close()
	}

	if err := awaitInProcess(ctx, client, run); err != nil {
		return Connection{}, errors.Join(err, release())
	}

	return Connection{
		Client: client,
		Close: func(context.Context) error {
			releaseErr := release()

			return errors.Join(run.err, releaseErr)
		},
	}, nil
}

// controllerRun is the result of the controller goroutine. err is readable
// once done is closed.
type controllerRun struct {
	done chan struct{}
	err  error
}

// runController runs the controller and turns a panic into an error, so a
// crash during the run still reaches the command.
func runController(
	ctx context.Context,
	controller runnable,
) (runErr error) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}

		ctxscope.GetLogger(ctx).Error(
			"in-process controller panicked",
			"panic", recovered,
			"stack", string(debug.Stack()),
		)

		runErr = ctxerrors.Wrapf(
			commerr.ErrFailed,
			"in-process controller panicked: %v",
			recovered,
		)
	}()

	if err := controller.Run(ctx); err != nil {
		return ctxerrors.Wrap(err, "run the in-process controller")
	}

	return nil
}

// awaitInProcess waits until the controller answers, fails, or the timeout
// passes. A controller that stops during startup reports why.
func awaitInProcess(
	ctx context.Context,
	client *controlclient.Client,
	run *controllerRun,
) error {
	readyCtx, cancel := context.WithTimeout(ctx, inProcessReadyTimeout)
	defer cancel()

	ticker := time.NewTicker(inProcessReadyPollInterval)
	defer ticker.Stop()

	for {
		// Checked before every probe: when the stop and the next tick are
		// both ready, select picks either one.
		if isStopped(run) {
			return startupFailure(run)
		}

		if probeInProcess(readyCtx, client) {
			return nil
		}

		select {
		case <-run.done:
			return startupFailure(run)
		case <-ticker.C:
		case <-readyCtx.Done():
			return ctxerrors.Wrap(
				readyCtx.Err(),
				"wait for the in-process controller",
			)
		}
	}
}

// probeInProcess asks the controller whether it is ready. An in-memory dial
// waits until the API accepts, which it never does while the controller is
// still starting, so each probe gets its own short deadline.
func probeInProcess(
	ctx context.Context,
	client *controlclient.Client,
) bool {
	probeCtx, cancel := context.WithTimeout(ctx, inProcessProbeTimeout)
	defer cancel()

	return client.Reachable(probeCtx)
}

func isStopped(run *controllerRun) bool {
	select {
	case <-run.done:
		return true
	default:
		return false
	}
}

func startupFailure(run *controllerRun) error {
	if run.err != nil {
		return ctxerrors.Wrap(run.err, "start the in-process controller")
	}

	return ctxerrors.Wrap(
		commerr.ErrTerminated,
		"the in-process controller stopped during startup",
	)
}
