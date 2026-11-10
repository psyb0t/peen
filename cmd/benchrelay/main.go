// Command benchrelay forwards loopback HTTP model traffic inside a networkless
// benchmark task container to the provider proxy's Unix socket.
//
// The benchmark adapter copies it into each task container next to the Peen
// binary. Peen calls the model at the loopback address, and the relay passes
// each request to the socket named by BENCH_SOCKET, which the host proxy
// serves. When BENCH_TRIAL is set, every request carries it in the
// X-Bench-Trial header, so the proxy can tell trials apart and apply the
// trial's fault schedule. See docs/benchmarks.md.
package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"github.com/psyb0t/ctxscope"
)

const (
	listenAddress     = "127.0.0.1:8765"
	socketEnvName     = "BENCH_SOCKET"
	trialEnvName      = "BENCH_TRIAL"
	trialHeader       = "X-Bench-Trial"
	networkTCP        = "tcp"
	networkUnix       = "unix"
	upstreamScheme    = "http"
	upstreamHost      = "provider"
	exitCodeFailed    = 1
	readHeaderTimeout = 30 * time.Second

	// flushImmediately makes the reverse proxy flush every write, so a
	// streamed model reply reaches Peen chunk by chunk.
	flushImmediately = -1
)

func main() {
	ctx := context.Background()
	logger := ctxscope.GetLogger(ctx)

	socketPath := os.Getenv(socketEnvName)
	if socketPath == "" {
		logger.Error("relay socket not configured", "env", socketEnvName)
		os.Exit(exitCodeFailed)
	}

	trial := os.Getenv(trialEnvName)

	listener, err := (&net.ListenConfig{}).Listen(ctx, networkTCP,
		listenAddress)
	if err != nil {
		logger.Error("relay listen failed", "err", err)
		os.Exit(exitCodeFailed)
	}

	logger.Info(
		"relay ready",
		"listen_address", listenAddress,
		"socket_path", socketPath,
		"trial", trial,
	)

	server := &http.Server{
		Handler:           newRelay(socketPath, trial),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	if err := server.Serve(listener); err != nil {
		logger.Error("relay serve failed", "err", err)
		os.Exit(exitCodeFailed)
	}
}

// newRelay builds the reverse proxy that sends every request to the Unix
// socket and stamps the trial header when one is configured.
func newRelay(socketPath, trial string) *httputil.ReverseProxy {
	target := &url.URL{Scheme: upstreamScheme, Host: upstreamHost}
	dialer := &net.Dialer{}

	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.Host = upstreamHost

			if trial != "" {
				request.Out.Header.Set(trialHeader, trial)
			}
		},
		Transport: &http.Transport{
			DialContext: func(
				ctx context.Context,
				_, _ string,
			) (net.Conn, error) {
				return dialer.DialContext(ctx, networkUnix, socketPath)
			},
			DisableCompression: true,
		},
		FlushInterval: flushImmediately,
	}
}
