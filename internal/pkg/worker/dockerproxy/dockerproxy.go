// Package dockerproxy is the read-only Docker Engine API proxy behind
// `docker: readonly`.
//
// A worker with that grant never receives the Docker socket. The controller
// runs one Server per worker on a Unix socket inside that session's own socket
// directory, and the worker's DOCKER_HOST points at it. The Server forwards a
// fixed allowlist of GET and HEAD endpoints to the controller's own daemon
// socket and answers everything else with 403 before it reaches the daemon.
//
// What a worker can still learn is deliberate and documented as a capability
// warning: every container, image, network, and volume on the host, their
// inspect documents with environment values redacted, resource stats, and the
// daemon's event stream. It cannot create, change, start, stop, exec into,
// attach to, or read the logs or files of anything.
package dockerproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

// errorBody is the Docker Engine API error shape, so a Docker client shows the
// refusal message as it would show a daemon error.
type errorBody struct {
	Message string `json:"message"`
}

// Server is one running read-only proxy.
type Server struct {
	socketPath string
	transport  *http.Transport
	http       *http.Server
	cancel     context.CancelFunc
	served     chan struct{}

	closeOnce sync.Once
	closeErr  error
}

// Start listens on socketPath and forwards allowed requests to the Docker
// daemon at upstreamSocketPath.
//
// A stale file at socketPath is removed first, because the path belongs to one
// session by construction and a Unix socket file outlives the process that
// made it. The socket is restricted to the controller's account. The returned
// Server serves until Close; ctx only scopes its logging.
func Start(
	ctx context.Context,
	socketPath, upstreamSocketPath string,
) (*Server, error) {
	if !filepath.IsAbs(socketPath) || !filepath.IsAbs(upstreamSocketPath) {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"the readonly Docker proxy needs absolute socket paths",
		)
	}

	if err := os.Remove(socketPath); err != nil &&
		!errors.Is(err, fs.ErrNotExist) {
		return nil, ctxerrors.Wrap(err, "remove a stale Docker proxy socket")
	}

	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(ctx, unixNetwork, socketPath)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "listen on the Docker proxy socket")
	}

	if err := os.Chmod(socketPath, socketMode); err != nil {
		closeListener(ctx, listener)

		return nil, ctxerrors.Wrap(
			err,
			"restrict the Docker proxy socket permissions",
		)
	}

	// Requests derive from this context, so cancelling it in Close also ends
	// the upstream half of a stream such as /events.
	baseCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	server := &Server{
		socketPath: socketPath,
		transport:  newUpstreamTransport(upstreamSocketPath),
		cancel:     cancel,
		served:     make(chan struct{}),
	}

	server.http = &http.Server{
		Handler:           server.handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext: func(net.Listener) context.Context {
			return baseCtx
		},
	}

	go server.serve(baseCtx, listener)

	ctxscope.GetLogger(ctx).Info(
		"readonly docker proxy started",
		"socket_path", socketPath,
	)

	return server, nil
}

// SocketPath is the socket the proxy listens on.
func (s *Server) SocketPath() string {
	return s.socketPath
}

// Close stops accepting, ends every open connection including a streaming
// /events request, and removes the socket file. It is safe to call more than
// once and returns the first call's result.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.close()
	})

	return s.closeErr
}

func (s *Server) close() error {
	s.cancel()

	closeErr := s.http.Close()

	<-s.served

	s.transport.CloseIdleConnections()

	if err := os.Remove(s.socketPath); err != nil &&
		!errors.Is(err, fs.ErrNotExist) {
		return ctxerrors.Wrap(err, "remove the Docker proxy socket")
	}

	if closeErr != nil {
		return ctxerrors.Wrap(closeErr, "close the Docker proxy server")
	}

	return nil
}

func (s *Server) serve(ctx context.Context, listener net.Listener) {
	defer close(s.served)

	defer func() {
		if recovered := recover(); recovered != nil {
			ctxscope.GetLogger(ctx).Error(
				"readonly docker proxy panicked",
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
		}
	}()

	err := s.http.Serve(listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}

	ctxscope.GetLogger(ctx).Error(
		"readonly docker proxy stopped serving",
		"socket_path", s.socketPath,
		"err", err,
	)
}

// handler refuses anything outside the allowlist and forwards the rest.
func (s *Server) handler() http.Handler {
	target := &url.URL{Scheme: upstreamScheme, Host: upstreamHost}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			// The proxy reads inspect bodies to redact them, so the daemon
			// must answer in plain JSON rather than compressed.
			request.Out.Header.Del(aichteeteapee.HeaderNameAcceptEncoding)
		},
		Transport:      s.transport,
		FlushInterval:  streamFlushImmediately,
		ModifyResponse: redactResponse,
		ErrorHandler:   handleUpstreamError,
	}

	return http.HandlerFunc(
		func(w http.ResponseWriter, request *http.Request) {
			reason := refusalReason(request)
			if reason == "" {
				proxy.ServeHTTP(w, request)

				return
			}

			ctxscope.GetLogger(request.Context()).Debug(
				"readonly docker proxy refused a request",
				"reason", reason,
				"method", request.Method,
				"path", request.URL.Path,
			)

			writeError(
				request.Context(),
				w,
				http.StatusForbidden,
				RefusalPrefix+reason,
			)
		},
	)
}

// redactResponse rewrites a successful container or image inspect so no
// environment value leaves the proxy. A body it cannot decode fails the
// request instead of passing the values through.
func redactResponse(response *http.Response) error {
	if !needsRedaction(response) {
		return nil
	}

	body, err := io.ReadAll(
		io.LimitReader(response.Body, maxInspectBodyBytes+1),
	)

	if closeErr := response.Body.Close(); closeErr != nil {
		ctxscope.GetLogger(response.Request.Context()).Debug(
			"closing the daemon inspect body failed",
			"err", closeErr,
		)
	}

	if err != nil {
		return ctxerrors.Wrap(err, "read the daemon inspect body")
	}

	if len(body) > maxInspectBodyBytes {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"the daemon inspect body is larger than %d bytes",
			maxInspectBodyBytes,
		)
	}

	redacted, err := redactInspectEnv(body)
	if err != nil {
		return err
	}

	response.Body = io.NopCloser(bytes.NewReader(redacted))
	response.ContentLength = int64(len(redacted))
	response.TransferEncoding = nil
	response.Header.Set(
		aichteeteapee.HeaderNameContentLength,
		strconv.Itoa(len(redacted)),
	)

	return nil
}

func needsRedaction(response *http.Response) bool {
	if response.StatusCode != http.StatusOK {
		return false
	}

	if response.Request.Method != http.MethodGet {
		return false
	}

	return classify(response.Request.URL.Path) == accessRedactEnv
}

// handleUpstreamError answers a request the proxy could not complete. The
// detail stays in the controller log, because it can name daemon internals.
func handleUpstreamError(
	w http.ResponseWriter,
	request *http.Request,
	err error,
) {
	logger := ctxscope.GetLogger(request.Context())

	// A client that went away, or a proxy being closed, is not a daemon
	// failure.
	if request.Context().Err() != nil {
		logger.Debug(
			"readonly docker proxy request ended early",
			"path", request.URL.Path,
			"err", err,
		)

		return
	}

	logger.Warn(
		"readonly docker proxy request failed",
		"reason", "upstream_failed",
		"path", request.URL.Path,
		"err", err,
	)

	writeError(
		request.Context(),
		w,
		http.StatusBadGateway,
		upstreamFailureMessage,
	)
}

func writeError(
	ctx context.Context,
	w http.ResponseWriter,
	status int,
	message string,
) {
	body, err := json.Marshal(errorBody{Message: message})
	if err != nil {
		ctxscope.GetLogger(ctx).Error(
			"encoding a docker proxy error failed",
			"err", err,
		)

		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set(
		aichteeteapee.HeaderNameContentType,
		aichteeteapee.ContentTypeJSON,
	)
	w.WriteHeader(status)

	if _, err := w.Write(body); err != nil {
		ctxscope.GetLogger(ctx).Debug(
			"writing a docker proxy error failed",
			"err", err,
		)
	}
}

// newUpstreamTransport dials the daemon socket for every request. It sets no
// response timeout, because /events and stats stream for as long as the
// client reads them.
func newUpstreamTransport(upstreamSocketPath string) *http.Transport {
	dialer := &net.Dialer{}

	return &http.Transport{
		// Without this the transport asks for gzip on its own, and an
		// inspect body read for redaction would depend on its transparent
		// decompression.
		DisableCompression: true,
		DialContext: func(
			ctx context.Context,
			_, _ string,
		) (net.Conn, error) {
			conn, err := dialer.DialContext(
				ctx,
				unixNetwork,
				upstreamSocketPath,
			)
			if err != nil {
				return nil, ctxerrors.Wrap(err, "dial the Docker daemon socket")
			}

			return conn, nil
		},
	}
}

func closeListener(ctx context.Context, listener net.Listener) {
	if err := listener.Close(); err != nil {
		ctxscope.GetLogger(ctx).Debug(
			"closing the docker proxy listener failed",
			"err", err,
		)
	}
}
