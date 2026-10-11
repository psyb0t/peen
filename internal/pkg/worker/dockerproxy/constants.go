package dockerproxy

import (
	"os"
	"time"
)

const (
	// socketMode keeps the proxy socket private to the controller's account,
	// which is also the account a Docker worker runs as.
	socketMode os.FileMode = 0o600

	// upstreamScheme and upstreamHost form the base of every forwarded
	// request. The connection is a Unix socket, so the host name is only
	// there to make a valid URL.
	upstreamScheme = "http"
	upstreamHost   = "docker"

	// unixNetwork is the network name of a Unix domain socket.
	unixNetwork = "unix"

	// readHeaderTimeout bounds how long a client may take to send request
	// headers. It does not bound a response, so `/events` and stats streams
	// stay open as long as the client reads them.
	readHeaderTimeout = 10 * time.Second

	// maxInspectBodyBytes bounds an inspect response the proxy reads whole to
	// redact it. A real inspect document is a few kilobytes.
	maxInspectBodyBytes = 16 << 20

	// streamFlushImmediately makes the reverse proxy flush after every write,
	// so an event reaches the worker when the daemon sends it.
	streamFlushImmediately = -1

	// RedactedValue replaces every environment variable value in container and
	// image inspect output. The variable name stays, so a worker can still see
	// which variables are set.
	RedactedValue = "<redacted>"

	// RefusalPrefix starts the message of every refused request.
	RefusalPrefix = "Peen's readonly Docker access refuses this request: "

	upstreamFailureMessage = "Peen's readonly Docker access could not " +
		"complete the request to the Docker daemon"
)
