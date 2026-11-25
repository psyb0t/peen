// Package emseepee mounts stateless MCP Streamable HTTP on a serbewr router.
package emseepee

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/aichteeteapee/serbewr"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

const (
	reasonInvalidOrigin = "invalid_origin"
	reasonInvalidHost   = "invalid_host"
	reasonCrossOrigin   = "cross_origin_rejected"
)

type routeConfig struct {
	sdkOptions      mcp.StreamableHTTPOptions
	publicOrigin    string
	allowedOrigins  []string
	originsSet      bool
	allowedHosts    []string
	hostsSet        bool
	writeTimeout    time.Duration
	noWriteDeadline bool
}

// RouteOption configures a Streamable HTTP route.
type RouteOption func(*routeConfig)

// WithSDKOptions sets the SDK options. The route always uses stateless mode
// and enables cancellation propagation for protocol versions that support it.
// WithAllowedHosts may set DisableLocalhostProtection.
func WithSDKOptions(options mcp.StreamableHTTPOptions) RouteOption {
	return func(config *routeConfig) {
		config.sdkOptions = options
	}
}

// WithPublicOrigin sets the exact browser Origin allowed behind a TLS proxy.
// By default, the request's own scheme and Host determine its origin.
// Combined with WithAllowedOrigins, it joins that allowlist.
func WithPublicOrigin(origin string) RouteOption {
	return func(config *routeConfig) {
		config.publicOrigin = origin
	}
}

// WithAllowedOrigins replaces the same-origin check with an allowlist of
// bare http(s) origins (scheme://host[:port]). A request without an Origin
// header passes, since only browsers send one. A request with one Origin
// must match an entry, compared with a lowercase scheme and host; several
// Origin headers are refused. A miss answers 403. The origin from
// WithPublicOrigin, when set, joins the list. The route also applies
// net/http cross-origin protection that trusts these origins. Calling it
// with no origins refuses every browser request except the public origin.
// Repeated calls add to the list.
func WithAllowedOrigins(origins ...string) RouteOption {
	return func(config *routeConfig) {
		config.originsSet = true
		config.allowedOrigins = append(config.allowedOrigins, origins...)
	}
}

// WithAllowedHosts accepts requests only when the Host authority names one
// of hosts, guarding against DNS rebinding. Entries are DNS names or IP
// addresses (IPv6 optionally in brackets) without a port, compared without
// case; a request on any valid port matches. A miss answers 421 Misdirected
// Request. When an entry is not a loopback host, the route disables the
// SDK's loopback-only Host check, because this allowlist replaces it.
// Repeated calls add to the list; at least one host is required.
func WithAllowedHosts(hosts ...string) RouteOption {
	return func(config *routeConfig) {
		config.hostsSet = true
		config.allowedHosts = append(config.allowedHosts, hosts...)
	}
}

// WithWriteTimeout sets a finite write deadline for this route, including
// streaming responses, and the same deadline on the request context. A zero
// value inherits the listener's write deadline. It cannot be combined with
// WithoutWriteDeadline.
func WithWriteTimeout(timeout time.Duration) RouteOption {
	return func(config *routeConfig) {
		config.writeTimeout = timeout
	}
}

// WithoutWriteDeadline clears the listener's write deadline for this route,
// so a tool call may stream for hours. Writers without deadline support are
// served as they are. Bound such routes with authentication and tool-level
// limits. It cannot be combined with WithWriteTimeout.
func WithoutWriteDeadline() RouteOption {
	return func(config *routeConfig) {
		config.noWriteDeadline = true
	}
}

// NewRoute builds one endpoint with SDK-owned HTTP method handling.
// Put it under a Router group to apply group auth on every request.
//
// Every method handler on a server getServer returns runs with a context
// that ends when the HTTP request ends, including for clients on protocol
// versions that predate the SDK's request cancellation. The route adds that
// receiving middleware once to each server it sees.
func NewRoute(
	path string,
	getServer func(*http.Request) *mcp.Server,
	opts ...RouteOption,
) (serbewr.RouteConfig, error) {
	if !strings.HasPrefix(path, "/") || getServer == nil {
		return serbewr.RouteConfig{}, ctxerrors.Wrap(
			aichteeteapee.ErrBadRequest, "MCP requires a route path and server",
		)
	}

	config := routeConfig{}
	for _, opt := range opts {
		opt(&config)
	}

	route, err := newRoute(config)
	if err != nil {
		return serbewr.RouteConfig{}, ctxerrors.Wrap(err, "configure MCP route")
	}

	config.sdkOptions.Stateless = true
	config.sdkOptions.PropagateRequestCancellation = true

	if route.nonLoopbackHosts {
		config.sdkOptions.DisableLocalhostProtection = true
	}

	var handler http.Handler = mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			server := getServer(r)
			route.cancellation.bind(server)

			return server
		},
		&config.sdkOptions,
	)

	if route.protection != nil {
		handler = route.protection.Handler(handler)
	}

	return serbewr.RouteConfig{
		Path: path,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			route.serve(w, r, handler)
		},
	}, nil
}

// route is the validated, immutable form of routeConfig.
type route struct {
	publicOrigin     string
	origins          map[string]struct{}
	hosts            map[string]struct{}
	nonLoopbackHosts bool
	protection       *http.CrossOriginProtection
	writeTimeout     time.Duration
	noWriteDeadline  bool
	cancellation     *cancellationBinder
}

func newRoute(c routeConfig) (*route, error) {
	if c.writeTimeout < 0 || c.sdkOptions.MaxRequestBodyBytes < 0 {
		return nil, ctxerrors.Wrap(
			aichteeteapee.ErrBadRequest, "negative MCP route limit",
		)
	}

	if c.noWriteDeadline && c.writeTimeout != 0 {
		return nil, ctxerrors.Wrap(aichteeteapee.ErrBadRequest,
			"MCP write timeout conflicts with no write deadline")
	}

	if c.publicOrigin != "" {
		origin, err := url.Parse(c.publicOrigin)
		if err != nil || !bareOrigin(origin, c.publicOrigin) {
			return nil, ctxerrors.Wrap(
				aichteeteapee.ErrBadRequest, "invalid MCP origin",
			)
		}
	}

	result := &route{
		publicOrigin:    c.publicOrigin,
		writeTimeout:    c.writeTimeout,
		noWriteDeadline: c.noWriteDeadline,
		cancellation:    newCancellationBinder(),
	}

	if err := result.addHosts(c); err != nil {
		return nil, err
	}

	if err := result.addOrigins(c); err != nil {
		return nil, err
	}

	return result, nil
}

func (r *route) addHosts(c routeConfig) error {
	if !c.hostsSet {
		return nil
	}

	if len(c.allowedHosts) == 0 {
		return ctxerrors.Wrap(aichteeteapee.ErrBadRequest, "no MCP hosts")
	}

	r.hosts = make(map[string]struct{}, len(c.allowedHosts))

	for _, entry := range c.allowedHosts {
		host, valid := normalizeHost(entry)
		if !valid {
			return ctxerrors.Wrap(
				aichteeteapee.ErrBadRequest, "invalid MCP host",
			)
		}

		r.hosts[host] = struct{}{}

		if !loopbackHost(host) {
			r.nonLoopbackHosts = true
		}
	}

	return nil
}

func (r *route) addOrigins(c routeConfig) error {
	if !c.originsSet {
		return nil
	}

	entries := c.allowedOrigins
	if c.publicOrigin != "" {
		entries = append([]string{c.publicOrigin}, entries...)
	}

	r.origins = make(map[string]struct{}, len(entries))
	r.protection = http.NewCrossOriginProtection()
	r.protection.SetDenyHandler(http.HandlerFunc(denyCrossOrigin))

	for _, entry := range entries {
		origin, valid := normalizeOrigin(entry)
		if !valid {
			return ctxerrors.Wrap(
				aichteeteapee.ErrBadRequest, "invalid MCP origin",
			)
		}

		r.origins[origin] = struct{}{}

		if err := r.protection.AddTrustedOrigin(origin); err != nil {
			return ctxerrors.Wrap(err, "trust MCP origin")
		}
	}

	return nil
}

func bareOrigin(origin *url.URL, raw string) bool {
	return (origin.Scheme == aichteeteapee.SchemeHTTPS ||
		origin.Scheme == aichteeteapee.SchemeHTTP) &&
		origin.Hostname() != "" && raw == origin.Scheme+"://"+origin.Host
}

func denyCrossOrigin(w http.ResponseWriter, r *http.Request) {
	ctxscope.GetLogger(r.Context()).Warn(
		"MCP cross-origin request rejected",
		aichteeteapee.FieldReason, reasonCrossOrigin,
	)
	aichteeteapee.WriteJSON(
		w, http.StatusForbidden, aichteeteapee.ErrorResponseForbidden,
	)
}

func (r *route) serve(
	w http.ResponseWriter,
	req *http.Request,
	handler http.Handler,
) {
	logger := ctxscope.GetLogger(req.Context())

	if r.hosts != nil && !hostAllowed(r.hosts, req.Host) {
		logger.Warn(
			"MCP host rejected", aichteeteapee.FieldReason, reasonInvalidHost,
		)
		aichteeteapee.WriteJSON(
			w, http.StatusMisdirectedRequest,
			aichteeteapee.ErrorResponseMisdirectedRequest,
		)

		return
	}

	if !r.allowsOrigin(req) {
		logger.Warn(
			"MCP origin rejected",
			aichteeteapee.FieldReason, reasonInvalidOrigin,
		)
		aichteeteapee.WriteJSON(
			w, http.StatusForbidden, aichteeteapee.ErrorResponseForbidden,
		)

		return
	}

	ctx, cancel, err := r.applyWriteDeadline(w, req)
	if err != nil {
		logger.Error("set MCP write deadline", "err", err)
		aichteeteapee.WriteJSON(
			w, http.StatusInternalServerError,
			aichteeteapee.ErrorResponseInternalServerError,
		)

		return
	}
	defer cancel()

	ctx = context.WithValue(ctx, callerContextKey{}, ctx)
	//nolint:contextcheck // ctx derives from req.Context() above.
	handler.ServeHTTP(w, req.WithContext(ctx))
}

// applyWriteDeadline sets the route's write deadline and returns the request
// context bounded by it.
func (r *route) applyWriteDeadline(
	w http.ResponseWriter,
	req *http.Request,
) (context.Context, context.CancelFunc, error) {
	controller := http.NewResponseController(w)

	if r.noWriteDeadline {
		err := controller.SetWriteDeadline(time.Time{})
		if err != nil && !errors.Is(err, http.ErrNotSupported) {
			return nil, nil, ctxerrors.Wrap(err, "clear write deadline")
		}

		return req.Context(), func() {}, nil
	}

	if r.writeTimeout == 0 {
		return req.Context(), func() {}, nil
	}

	deadline := time.Now().Add(r.writeTimeout)
	if err := controller.SetWriteDeadline(deadline); err != nil {
		return nil, nil, ctxerrors.Wrap(err, "set write deadline")
	}

	ctx, cancel := context.WithDeadline(req.Context(), deadline)

	return ctx, cancel, nil
}

func (r *route) allowsOrigin(req *http.Request) bool {
	origins := req.Header.Values(aichteeteapee.HeaderNameOrigin)
	if len(origins) == 0 {
		return true
	}

	if len(origins) > 1 {
		return false
	}

	if r.origins != nil {
		origin, valid := normalizeOrigin(origins[0])
		if !valid {
			return false
		}

		_, allowed := r.origins[origin]

		return allowed
	}

	expected := r.publicOrigin
	if expected == "" {
		scheme := aichteeteapee.SchemeHTTP
		if req.TLS != nil {
			scheme = aichteeteapee.SchemeHTTPS
		}

		expected = scheme + "://" + req.Host
	}

	return origins[0] == expected
}
