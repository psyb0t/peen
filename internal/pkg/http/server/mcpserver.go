package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/aichteeteapee/serbewr/middleware"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

type mcpRequestContextKey struct{}

func (s *Server) configureMCPHandler() error {
	if s.deps.MCPServer == nil {
		s.mcpHTTPHandler = http.NotFoundHandler()

		return nil
	}

	protection, err := s.deps.MCPPolicy.CrossOriginProtection()
	if err != nil {
		return ctxerrors.Wrap(err, "create MCP cross-origin guard")
	}

	s.deps.MCPServer.AddReceivingMiddleware(mcpCallerCancellation)

	maximum := s.deps.MaxFileAPIBytes
	if maximum <= 0 {
		maximum = defaultMaxFileAPIBytes
	}

	bodyLimit := max(s.deps.WebSocketReadLimit,
		int64(base64.StdEncoding.EncodedLen(int(maximum)))+
			fileRequestBodyOverhead)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.deps.MCPServer },
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			MaxRequestBodyBytes:          bodyLimit,
			PropagateRequestCancellation: true,
			// The outer allowlist still checks nonloopback hosts.
			DisableLocalhostProtection: s.deps.MCPPolicy.HasNonLoopbackHosts(),
		},
	)
	s.mcpHTTPHandler = s.authenticate(s.guardMCP(protection.Handler(handler)))

	return nil
}

func (s *Server) guardMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.deps.MCPPolicy.AllowsHost(r.Host) {
			http.Error(w, http.StatusText(http.StatusMisdirectedRequest),
				http.StatusMisdirectedRequest)

			return
		}

		origins := r.Header.Values("Origin")
		if len(origins) > 1 ||
			(len(origins) == 1 && !s.deps.MCPPolicy.AllowsOrigin(origins[0])) {
			http.Error(w, http.StatusText(http.StatusForbidden),
				http.StatusForbidden)

			return
		}

		if err := prepareLongResponse(w); err != nil {
			ctxscope.GetLogger(r.Context()).Error(
				"MCP response deadline failed", "err", err)
			http.Error(w, internalServerErrorMessage,
				http.StatusInternalServerError)

			return
		}

		ctx := context.WithValue(
			r.Context(), mcpRequestContextKey{}, r.Context(),
		)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// The SDK detaches legacy HTTP contexts. Preserve cancellation of our turn
// even for protocol versions predating its propagation option.
func mcpCallerCancellation(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (
		mcp.Result, error,
	) {
		caller, ok := ctx.Value(mcpRequestContextKey{}).(context.Context)
		if !ok {
			return next(ctx, method, request)
		}

		bound, cancel := context.WithCancel(ctx)
		defer cancel()

		//nolint:contextcheck // Original HTTP context owns cancellation.
		stop := context.AfterFunc(caller, cancel)
		defer stop()

		if caller.Err() != nil {
			cancel()
		}

		return next(bound, method, request)
	}
}

// MCP preflight requests must pass the same policy and authentication checks.
func protocolCORS(next http.Handler) http.Handler {
	ordinary := middleware.CORS()(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == mcpBaseURL ||
			strings.HasPrefix(r.URL.Path, mcpBaseURL+"/") {
			next.ServeHTTP(w, r)

			return
		}

		ordinary.ServeHTTP(w, r)
	})
}
