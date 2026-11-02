// Command benchrelay forwards loopback TCP model traffic inside a networkless
// benchmark task container to the provider proxy's Unix socket.
//
// The benchmark adapter copies it into each task container next to the Peen
// binary. Peen calls the model at the loopback address, and the relay passes
// the bytes to the socket named by BENCH_SOCKET, which the host proxy serves.
// See docs/benchmarks.md.
package main

import (
	"context"
	"io"
	"net"
	"os"
	"sync"

	"github.com/psyb0t/ctxscope"
)

const (
	listenAddress  = "127.0.0.1:8765"
	socketEnvName  = "BENCH_SOCKET"
	networkTCP     = "tcp"
	networkUnix    = "unix"
	exitCodeFailed = 1
	copyDirections = 2
	sideClient     = "client"
	sideUpstream   = "upstream"
)

func main() {
	ctx := context.Background()
	logger := ctxscope.GetLogger(ctx)

	socketPath := os.Getenv(socketEnvName)
	if socketPath == "" {
		logger.Error("relay socket not configured", "env", socketEnvName)
		os.Exit(exitCodeFailed)
	}

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
	)

	for {
		client, err := listener.Accept()
		if err != nil {
			logger.Warn("relay accept failed", "err", err)

			continue
		}

		go forward(ctx, client, socketPath)
	}
}

func forward(ctx context.Context, client net.Conn, socketPath string) {
	logger := ctxscope.GetLogger(ctx)

	defer closeConn(ctx, client, sideClient)

	upstream, err := (&net.Dialer{}).DialContext(ctx, networkUnix, socketPath)
	if err != nil {
		logger.Error("relay upstream dial failed", "err", err)

		return
	}
	defer closeConn(ctx, upstream, sideUpstream)

	var wg sync.WaitGroup

	wg.Add(copyDirections)

	go func() {
		defer wg.Done()

		copyAndCloseWrite(ctx, upstream, client, sideUpstream)
	}()

	go func() {
		defer wg.Done()

		copyAndCloseWrite(ctx, client, upstream, sideClient)
	}()

	wg.Wait()
}

// copyAndCloseWrite copies until source ends, then half-closes destination so
// the other side sees the end of the stream while its reply still flows.
func copyAndCloseWrite(
	ctx context.Context,
	destination, source net.Conn,
	side string,
) {
	logger := ctxscope.GetLogger(ctx)

	if _, err := io.Copy(destination, source); err != nil {
		logger.Debug("relay copy ended", "side", side, "err", err)
	}

	closer, ok := destination.(interface{ CloseWrite() error })
	if !ok {
		return
	}

	if err := closer.CloseWrite(); err != nil {
		logger.Debug("relay close write failed", "side", side, "err", err)
	}
}

func closeConn(ctx context.Context, conn net.Conn, side string) {
	if err := conn.Close(); err != nil {
		ctxscope.GetLogger(ctx).Debug("relay close failed", "side", side,
			"err", err)
	}
}
