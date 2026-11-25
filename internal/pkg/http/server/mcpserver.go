package server

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	emseepee "github.com/psyb0t/aichteeteapee/serbewr/em-see-pee"
	"github.com/psyb0t/aichteeteapee/serbewr/middleware"
	"github.com/psyb0t/ctxerrors"
)

// configureMCPHandler mounts the MCP control server behind Peen's bearer
// authentication. The emseepee route then checks Host and Origin against
// the configured policy, clears the listener write deadline so a tool call
// can outlive it, and binds every tool call's context to its HTTP request.
func (s *Server) configureMCPHandler() error {
	if s.deps.MCPServer == nil {
		s.mcpHTTPHandler = http.NotFoundHandler()

		return nil
	}

	maximum := s.deps.MaxFileAPIBytes
	if maximum <= 0 {
		maximum = defaultMaxFileAPIBytes
	}

	bodyLimit := max(s.deps.WebSocketReadLimit,
		int64(base64.StdEncoding.EncodedLen(int(maximum)))+
			fileRequestBodyOverhead)

	protocol := s.deps.MCPServer

	route, err := emseepee.NewRoute(
		mcpBaseURL,
		func(*http.Request) *mcp.Server { return protocol },
		emseepee.WithAllowedHosts(s.deps.MCPPolicy.Hosts()...),
		emseepee.WithAllowedOrigins(s.deps.MCPPolicy.Origins()...),
		emseepee.WithoutWriteDeadline(),
		emseepee.WithSDKOptions(mcp.StreamableHTTPOptions{
			MaxRequestBodyBytes: bodyLimit,
		}),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "create MCP route")
	}

	s.mcpHTTPHandler = s.authenticate(route.Handler)

	return nil
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
