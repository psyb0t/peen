package emseepee

import (
	"context"
	"runtime"
	"sync"
	"weak"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callerContextKey holds the HTTP request context, bounded by the route's
// write deadline, for callerCancellation.
type callerContextKey struct{}

// cancellationBinder adds callerCancellation to each server once. Entries
// are keyed by weak pointers and removed when their server is collected, so
// a getServer that builds a server per request does not grow the map.
type cancellationBinder struct {
	middleware mcp.Middleware
	servers    sync.Map // weak.Pointer[mcp.Server] -> *sync.Once
}

func newCancellationBinder() *cancellationBinder {
	return &cancellationBinder{middleware: callerCancellation}
}

func (b *cancellationBinder) bind(server *mcp.Server) {
	if server == nil {
		return
	}

	key := weak.Make(server)
	value, loaded := b.servers.LoadOrStore(key, &sync.Once{})

	once, ok := value.(*sync.Once)
	if !ok {
		return
	}

	// Concurrent first requests wait here until the middleware is added.
	once.Do(func() { server.AddReceivingMiddleware(b.middleware) })

	if !loaded {
		runtime.AddCleanup(server, func(key weak.Pointer[mcp.Server]) {
			b.servers.Delete(key)
		}, key)
	}
}

// callerCancellation ends the method context when the HTTP request ends.
// The SDK detaches the method context from the request for protocol
// versions that predate its own cancellation propagation, so a client that
// disconnects would otherwise leave its tool call running.
func callerCancellation(next mcp.MethodHandler) mcp.MethodHandler {
	return func(
		ctx context.Context, method string, request mcp.Request,
	) (mcp.Result, error) {
		caller, ok := ctx.Value(callerContextKey{}).(context.Context)
		if !ok {
			return next(ctx, method, request)
		}

		bound, cancel := context.WithCancel(ctx)
		defer cancel()

		//nolint:contextcheck // The HTTP request context owns cancellation.
		stop := context.AfterFunc(caller, cancel)
		defer stop()

		// AfterFunc runs cancel asynchronously for an ended caller; cancel
		// now so the handler never starts with a live context.
		if caller.Err() != nil {
			cancel()
		}

		return next(bound, method, request)
	}
}
