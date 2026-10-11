package config

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultDockerSocketPath is the host socket a controller uses to create
	// worker containers. It is the controller's own grant, separate from the
	// socket a `docker: full` profile mounts into a worker.
	DefaultDockerSocketPath = "/var/run/docker.sock"

	// dockerHostEnvKey is the conventional Docker client environment variable.
	dockerHostEnvKey = "DOCKER_HOST"

	// dockerHostUnixPrefix names a local socket in DOCKER_HOST. Peen launches
	// workers only through a local socket.
	dockerHostUnixPrefix = "unix://"

	// dockerHostTCPPrefix names a remote daemon in DOCKER_HOST. Peen cannot
	// launch workers through one, but a reachable one is host root for a
	// control agent all the same, so it counts as Docker authority.
	dockerHostTCPPrefix = "tcp://"

	// dockerPingURL is the Docker Engine API liveness endpoint. The host name
	// is only there to make a valid URL, because the connection is a Unix
	// socket.
	dockerPingURL = "http://docker/_ping"

	// dockerProbeTimeout bounds one reachability probe, so a daemon that hangs
	// cannot stall startup.
	dockerProbeTimeout = 2 * time.Second

	// workerSocketDirectoryName is where session worker sockets live when the
	// operator names no directory.
	workerSocketDirectoryName = "workers"
)

// dockerEnvironment is what a Docker daemon lookup reads from outside the
// configuration: DOCKER_HOST and the platform default socket.
type dockerEnvironment struct {
	host          string
	defaultSocket string
}

// DockerAuthority is the Docker daemon a controller can reach, resolved once so
// the agent space rules and the worker launcher agree on one answer.
type DockerAuthority struct {
	// SocketPath is the first local socket that answers the Docker API. Empty
	// when no local socket does.
	SocketPath string

	// RemoteHost is a reachable `tcp://` DOCKER_HOST. Set only when no local
	// socket answers.
	RemoteHost string
}

// Present reports whether any Docker daemon is reachable.
func (a DockerAuthority) Present() bool {
	return a.SocketPath != "" || a.RemoteHost != ""
}

// String names the reachable daemon for an error message.
func (a DockerAuthority) String() string {
	if a.SocketPath != "" {
		return a.SocketPath
	}

	return a.RemoteHost
}

// WithDockerEnvironment returns a copy that finds the Docker daemon from the
// given DOCKER_HOST value and platform default socket instead of the process
// environment and /var/run/docker.sock.
//
// A test that must not depend on the Docker daemon of the machine running it
// uses this. PEEN_DOCKER_SOCKET alone cannot do that, because it never hides
// the other candidates.
func (c Config) WithDockerEnvironment(host, defaultSocket string) Config {
	c.dockerEnv = &dockerEnvironment{host: host, defaultSocket: defaultSocket}

	return c
}

// dockerHost is the trimmed DOCKER_HOST value.
func (c Config) dockerHost() string {
	if c.dockerEnv != nil {
		return strings.TrimSpace(c.dockerEnv.host)
	}

	return strings.TrimSpace(os.Getenv(dockerHostEnvKey))
}

// defaultDockerSocket is the platform default socket path.
func (c Config) defaultDockerSocket() string {
	if c.dockerEnv != nil {
		return c.dockerEnv.defaultSocket
	}

	return DefaultDockerSocketPath
}

// WorkerSocketRoot is the directory holding one private directory per session
// worker.
//
// It is controller runtime state, so it defaults under PEEN_STATE_DIR and never
// under PEEN_CONFIG_DIR. No worker receives this root: a Docker worker is given
// only its own session's directory beneath it, so it never learns another
// session's socket path.
func (c Config) WorkerSocketRoot() string {
	if c.WorkerSocketDirectory != "" {
		return c.WorkerSocketDirectory
	}

	return filepath.Join(c.StateDirectory, workerSocketDirectoryName)
}

// dockerSocketCandidates lists every local socket a Docker client of this
// process could use, in priority order: the explicit setting, a `unix://`
// DOCKER_HOST, then the platform default.
func (c Config) dockerSocketCandidates() []string {
	candidates := make([]string, 0, 3) //nolint:mnd // Three sources.

	add := func(path string) {
		if path == "" || slices.Contains(candidates, path) {
			return
		}

		candidates = append(candidates, path)
	}

	add(c.DockerSocket)

	if socket, isUnix := strings.CutPrefix(
		c.dockerHost(),
		dockerHostUnixPrefix,
	); isUnix {
		add(socket)
	}

	add(c.defaultDockerSocket())

	return candidates
}

// DockerAuthority probes every candidate daemon and reports what answers.
//
// Every candidate is checked, not just the first one that is set. An operator
// who points PEEN_DOCKER_SOCKET at a missing path, or DOCKER_HOST at a remote
// daemon, has not hidden the platform default socket from a control agent, and
// the agent space rules must see whatever the agent could reach.
//
// Reachable means the daemon answers the Docker API, not that a file exists. A
// socket file the controller's user cannot open is unusable to the agent too,
// since it runs as the same user, so it does not count.
func (c Config) DockerAuthority() DockerAuthority {
	for _, path := range c.dockerSocketCandidates() {
		if dockerSocketAnswers(path) {
			return DockerAuthority{SocketPath: path}
		}
	}

	host := c.dockerHost()

	address, isTCP := strings.CutPrefix(host, dockerHostTCPPrefix)
	if isTCP && remoteDockerAnswers(address) {
		return DockerAuthority{RemoteHost: host}
	}

	return DockerAuthority{}
}

// DockerSocketPath is the local socket the controller launches workers
// through: the first candidate that answers the Docker API. Empty when none
// does.
func (c Config) DockerSocketPath() string {
	return c.DockerAuthority().SocketPath
}

// HasDockerAuthority reports whether this controller can reach a Docker
// daemon.
//
// The agent space rules depend on it at startup: with a daemon the control
// agent space is refused, and without one Docker agent spaces cannot run. A
// deployment without authority refuses Docker agent spaces outright and never
// falls back to a control worker.
func (c Config) HasDockerAuthority() bool {
	return c.DockerAuthority().Present()
}

// dockerSocketAnswers reports whether a Docker daemon answers /_ping on the
// socket. A missing path, a stale socket file, a permission failure, and a
// listener that is not Docker all read as unreachable.
func dockerSocketAnswers(path string) bool {
	dialer := &net.Dialer{}

	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", path)
	}

	client := &http.Client{
		Timeout: dockerProbeTimeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       dial,
		},
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		dockerProbeTimeout,
	)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		dockerPingURL,
		http.NoBody,
	)
	if err != nil {
		return false
	}

	response, err := client.Do(request)
	if err != nil {
		return false
	}

	// The status is all the probe reads, so a failed close changes nothing.
	defer func() { _ = response.Body.Close() }()

	return response.StatusCode == http.StatusOK
}

// remoteDockerAnswers reports whether a TCP endpoint accepts a connection.
//
// It does not speak the Docker API. A daemon behind TLS would refuse an
// unauthenticated ping yet still be reachable to an agent holding the
// operator's client certificates, so a successful connection is enough to
// count the endpoint as Docker authority.
func remoteDockerAnswers(address string) bool {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		dockerProbeTimeout,
	)
	defer cancel()

	dialer := &net.Dialer{}

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}

	// Only the connect mattered, so a failed close changes nothing.
	_ = conn.Close()

	return true
}
