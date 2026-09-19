package config

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultDockerSocketPath is the host socket a controller uses to create
	// worker containers. It is the controller's own grant, separate from the
	// socket a host-like profile may mount into a worker.
	DefaultDockerSocketPath = "/var/run/docker.sock"

	// dockerHostEnvKey is the conventional Docker client environment variable.
	// Peen reads it only to locate a local socket.
	dockerHostEnvKey = "DOCKER_HOST"

	// dockerHostUnixPrefix is the only DOCKER_HOST form this build resolves to
	// a socket path. A remote daemon is not a local socket, so it is not
	// treated as one.
	dockerHostUnixPrefix = "unix://"

	// workerSocketDirectoryName is where session worker sockets live when the
	// operator names no directory.
	workerSocketDirectoryName = "workers"
)

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

// DockerSocketPath is the controller's own Docker socket.
//
// An explicit setting wins. Otherwise a `unix://` DOCKER_HOST names the socket,
// and failing that the platform default is used. A DOCKER_HOST naming anything
// other than a Unix socket yields an empty path, because this build only
// launches workers through a local socket it can verify.
func (c Config) DockerSocketPath() string {
	if c.DockerSocket != "" {
		return c.DockerSocket
	}

	host := strings.TrimSpace(os.Getenv(dockerHostEnvKey))
	if host == "" {
		return DefaultDockerSocketPath
	}

	if !strings.HasPrefix(host, dockerHostUnixPrefix) {
		return ""
	}

	return strings.TrimPrefix(host, dockerHostUnixPrefix)
}

// HasDockerAuthority reports whether this controller can create worker
// containers.
//
// It is a real check against the socket rather than a configuration flag,
// because an operator naming a Docker profile needs to know now, at startup,
// whether that profile can ever run. A deployment without authority refuses
// Docker profiles outright and never falls back to a native worker.
func (c Config) HasDockerAuthority() bool {
	path := c.DockerSocketPath()
	if path == "" {
		return false
	}

	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeSocket != 0
}
