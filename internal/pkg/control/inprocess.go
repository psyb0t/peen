package control

import (
	"net"
	"sync"
)

// InProcessEndpoint carries the listener a controller serves its API on when
// it must not be reachable from the network.
//
// Servicepack builds control-api from a no-argument factory, so the one-shot
// command, which runs the controller inside its own process, sets the listener
// here before the services start. An empty endpoint means control-api binds
// PEEN_HTTP_LISTEN_ADDRESS and PEEN_METRICS_LISTEN_ADDRESS as usual.
type InProcessEndpoint struct {
	mutex    sync.Mutex
	listener net.Listener
}

// sharedInProcessEndpoint is the one endpoint the registered services read.
//
//nolint:gochecknoglobals // The service factory signature leaves no other seam.
var sharedInProcessEndpoint = &InProcessEndpoint{}

// SharedInProcessEndpoint returns the process-wide in-process endpoint.
func SharedInProcessEndpoint() *InProcessEndpoint {
	return sharedInProcessEndpoint
}

// Set makes control-api serve on listener instead of the configured address.
// It must be called before the controller starts.
func (e *InProcessEndpoint) Set(listener net.Listener) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	e.listener = listener
}

// Clear returns the endpoint to the configured network listeners.
func (e *InProcessEndpoint) Clear() {
	e.Set(nil)
}

// Listener returns the in-process listener, or nil when none is set.
func (e *InProcessEndpoint) Listener() net.Listener {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	return e.listener
}
