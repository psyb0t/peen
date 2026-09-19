// Package controlapi serves the control surface that control-core opened.
//
// It owns the REST listener, the global WebSocket hub, the metrics listener,
// and the readiness gate. It creates no durable state of its own.
package controlapi

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/control"
	peenhttp "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	// ServiceName is the servicepack registration key and log scope.
	ServiceName = control.APIServiceName

	networkTCP       = "tcp"
	serveWorkerCount = 2

	// readinessProbeInterval bounds how often the service checks whether its
	// own listener is accepting connections.
	readinessProbeInterval = 20 * time.Millisecond
	readinessProbeTimeout  = 2 * time.Second
)

type serviceDependencies struct {
	handoff *control.Handoff
	listen  func(
		ctx context.Context,
		network string,
		address string,
	) (net.Listener, error)
	dial func(
		ctx context.Context,
		network string,
		address string,
	) (net.Conn, error)
}

// ControlAPI serves the control REST API, the global WebSocket feed, and the
// metrics endpoint over the state control-core owns.
type ControlAPI struct {
	dependencies serviceDependencies

	readyOnce sync.Once
	ready     chan struct{}
}

// New builds the service through servicepack's generated registration.
func New() (*ControlAPI, error) {
	return newControlAPI(serviceDependencies{}), nil
}

func newControlAPI(dependencies serviceDependencies) *ControlAPI {
	if dependencies.handoff == nil {
		dependencies.handoff = control.SharedHandoff()
	}

	if dependencies.listen == nil {
		dependencies.listen = (&net.ListenConfig{}).Listen
	}

	if dependencies.dial == nil {
		dependencies.dial = (&net.Dialer{}).DialContext
	}

	return &ControlAPI{
		dependencies: dependencies,
		ready:        make(chan struct{}),
	}
}

func (s *ControlAPI) Name() string {
	return ServiceName
}

// Dependencies orders this service after control-core, so the durable store
// and runtime exist before the API accepts a request.
func (s *ControlAPI) Dependencies() []string {
	return []string{control.CoreServiceName}
}

// Ready closes once the configured endpoint is accepting connections. A client
// that auto-starts the controller can then treat readiness as "the API answers"
// rather than "a goroutine was scheduled".
func (s *ControlAPI) Ready() <-chan struct{} {
	return s.ready
}

// Run serves the control API until the context ends.
func (s *ControlAPI) Run(ctx context.Context) error {
	ctx = ctxscope.Set(ctx, ctxscope.Attr("service", ServiceName))
	logger := ctxscope.GetLogger(ctx)

	core, err := s.dependencies.handoff.Await(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "await the control core")
	}

	server, err := peenhttp.New(peenhttp.Dependencies{
		Runtime:        core.Runtime,
		Sessions:       core.Sessions,
		Turns:          core.Turns,
		APIToken:       core.Config.APIToken,
		ListenAddress:  core.Config.HTTPListenAddress,
		Metrics:        core.Metrics,
		ServiceContext: func() context.Context { return ctx },
	})
	if err != nil {
		return ctxerrors.Wrap(err, "create control API server")
	}

	// Registering the sink here is what connects a worker's durable write to
	// the live feed. Until it happens the controller still records everything;
	// it simply has nowhere to deliver yet.
	core.Events.SetSink(func(
		_ context.Context,
		sessionID uuid.UUID,
		events []session.EventInput,
	) {
		server.BroadcastDurableSessionEvents(sessionID, events)
	})

	metricsServer, err := metrics.NewServer(core.Metrics)
	if err != nil {
		return ctxerrors.Wrap(err, "create metrics server")
	}

	metricsListener, err := s.dependencies.listen(
		ctx,
		networkTCP,
		core.Config.MetricsListenAddress,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "open metrics listener")
	}

	logger.Info(
		"control API initialized",
		"listen_address", core.Config.HTTPListenAddress,
		"metrics_listen_address", core.Config.MetricsListenAddress,
		"api_token_configured", core.Config.APIToken != "",
	)

	go s.signalReadyWhenListening(ctx, core.Config.HTTPListenAddress)

	return serveBoth(ctx, server, metricsServer, metricsListener)
}

// signalReadyWhenListening closes the ready channel once the API endpoint
// answers a connection, which is what a dependent service and an auto-starting
// client actually need to know.
func (s *ControlAPI) signalReadyWhenListening(
	ctx context.Context,
	listenAddress string,
) {
	probeCtx, cancel := context.WithTimeout(ctx, readinessProbeTimeout)
	defer cancel()

	address := control.DialableAddress(listenAddress)

	ticker := time.NewTicker(readinessProbeInterval)
	defer ticker.Stop()

	for {
		connection, err := s.dependencies.dial(probeCtx, networkTCP, address)
		if err == nil {
			if closeErr := connection.Close(); closeErr != nil {
				ctxscope.GetLogger(ctx).Warn(
					"closing the readiness probe connection failed",
					"err", closeErr,
				)
			}

			s.readyOnce.Do(func() { close(s.ready) })

			return
		}

		select {
		case <-ticker.C:
		case <-probeCtx.Done():
			ctxscope.GetLogger(ctx).Warn(
				"control API readiness probe gave up",
				"reason", "listener_not_accepting",
				"listen_address", listenAddress,
				"err", err,
			)

			return
		}
	}
}

func serveBoth(
	ctx context.Context,
	apiServer *peenhttp.Server,
	metricsServer *metrics.Server,
	metricsListener net.Listener,
) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	serveErrors := make(chan error, serveWorkerCount)
	go func() {
		serveErrors <- apiServer.Serve(serveCtx)
	}()
	go func() {
		serveErrors <- metricsServer.ServeListener(serveCtx, metricsListener)
	}()

	firstErr := <-serveErrors

	cancel()

	secondErr := <-serveErrors

	return errors.Join(firstErr, secondErr)
}

func (s *ControlAPI) Stop(ctx context.Context) error {
	serviceCtx := ctxscope.Set(ctx, ctxscope.Attr("service", ServiceName))

	ctxscope.GetLogger(serviceCtx).Info("stopping control API")

	return nil
}
