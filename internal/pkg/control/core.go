package control

import (
	"context"
	"sync"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/supervisor"
)

// Core is the durable state and runtime that control-core opens and control-api
// serves. One controller owns exactly one Core.
type Core struct {
	Config    peenconfig.Config
	Upstreams []peenconfig.Upstream
	Runtime   *agent.Runtime
	Store     *session.Store
	Handle    *db.Handle
	Sessions  *Registry

	// Profiles are the deployment's allowed execution profiles. The API reads
	// them so a client can see what it may name.
	Profiles *worker.ProfileSet

	// Workers supervises this controller's session workers. Shutdown stops
	// them through it, so no worker outlives the controller that launched it.
	Workers *supervisor.Supervisor

	// Turns routes an accepted client message to its session's worker. The
	// control plane routes work; it does not run the model loop.
	Turns *TurnRouter

	// Events carries durable worker events to the live feed. control-core
	// creates it and control-api registers the delivery path, so a record is
	// written before any client can see it.
	Events *EventRelay

	Metrics *metrics.Metrics
}

func (c *Core) validate() error {
	if c.Runtime == nil || c.Store == nil || c.Handle == nil ||
		c.Sessions == nil || c.Profiles == nil || c.Metrics == nil ||
		c.Workers == nil || c.Turns == nil || c.Events == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "control core")
	}

	return nil
}

// Handoff passes one Core from the service that opens it to the service that
// serves it.
//
// Servicepack builds each service from a no-argument factory, so two services
// in one process cannot receive a shared dependency through a constructor. The
// dependency graph orders them, control-api after control-core, and the
// readiness gate makes the Core present before control-api runs. Await still
// blocks rather than trusting that order, so a misconfigured graph surfaces as
// a timeout instead of a nil dereference.
type Handoff struct {
	mutex     sync.Mutex
	core      *Core
	published chan struct{}
}

// NewHandoff builds an empty handoff. Tests use one of these instead of the
// process-wide handoff so scenarios stay independent.
func NewHandoff() *Handoff {
	return &Handoff{published: make(chan struct{})}
}

// sharedHandoff is the one handoff the registered services use. It is package
// state because the service factories that need it take no arguments.
//
//nolint:gochecknoglobals // The service factory signature leaves no other seam.
var sharedHandoff = NewHandoff()

// SharedHandoff returns the process-wide handoff between the control services.
func SharedHandoff() *Handoff {
	return sharedHandoff
}

// Publish records the Core and releases every waiter. Publishing twice is a
// wiring bug, because one process runs one controller.
func (h *Handoff) Publish(core *Core) error {
	if core == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "control core")
	}

	if err := core.validate(); err != nil {
		return err
	}

	h.mutex.Lock()
	defer h.mutex.Unlock()

	if h.core != nil {
		return ctxerrors.Wrap(
			commerr.ErrAlreadyExists,
			"control core is already published",
		)
	}

	h.core = core
	close(h.published)

	return nil
}

// Await returns the published Core, blocking until control-core publishes one
// or the context ends.
func (h *Handoff) Await(ctx context.Context) (*Core, error) {
	h.mutex.Lock()
	published := h.published
	h.mutex.Unlock()

	select {
	case <-published:
		h.mutex.Lock()
		defer h.mutex.Unlock()

		if h.core == nil {
			return nil, ctxerrors.Wrap(
				commerr.ErrNotFound,
				"control core was cleared before it was read",
			)
		}

		return h.core, nil
	case <-ctx.Done():
		return nil, ctxerrors.Wrap(
			ctx.Err(),
			"wait for the control core to be published",
		)
	}
}

// Clear drops the published Core so the next controller in this process starts
// from nothing. control-core clears it as it shuts down, and tests clear it
// between scenarios.
func (h *Handoff) Clear() {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if h.core == nil {
		return
	}

	h.core = nil
	h.published = make(chan struct{})
}
