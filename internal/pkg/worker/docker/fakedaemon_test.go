package docker_test

import (
	"context"
	"io"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
)

// fakeDaemon records what the launcher asked the daemon to do. It never speaks
// to a real daemon, so these tests need no socket and start no container.
type fakeDaemon struct {
	created docker.CreateRequest
	stdin   []byte
	started []string
	stopped []string
	removed []string
	labels  map[string]string
	running bool

	// repoDigests is what the daemon reports for the worker image. Nil means an
	// image the daemon knows only locally, which records no repository digest.
	repoDigests []string

	// loggedContainers records which containers the launcher followed, so a
	// test can prove a Docker worker's records reach the controller's sinks.
	loggedContainers []string

	// logPayload is the already demultiplexed output the follow returns.
	logPayload string

	// logErr fails the follow, which must degrade the audit trail without
	// failing the worker.
	logErr error

	// pulled records the references the launcher fetched, so a test can prove a
	// missing image is pulled rather than failing the launch.
	pulled []string

	// absentUntilPulled makes the first inspect report the image is missing,
	// which is what a host that has never seen it answers.
	absentUntilPulled bool

	// pullErr fails the pull.
	pullErr error

	// createErr, attachErr, startErr, and inspectErr fail those calls, which
	// drives each launch abandon path.
	createErr  error
	attachErr  error
	startErr   error
	inspectErr error

	// calls is the order of lifecycle calls, shared with fakeProxy, so a test
	// can prove the proxy lives exactly as long as the container.
	calls []string

	// staleName is a container that already holds the worker name before the
	// launch, as one a crashed controller left behind does, with its labels
	// and running state.
	staleName    string
	staleLabels  map[string]string
	staleRunning bool
}

const (
	callProxyStart = "proxyStart"
	callProxyClose = "proxyClose"
	callCreate     = "create"
	callRemove     = "remove"
)

// fakeProxy stands in for the readonly proxy and records its lifetime in the
// fake daemon's call order.
type fakeProxy struct {
	daemon   *fakeDaemon
	closeErr error
}

func (p *fakeProxy) Close() error {
	p.daemon.calls = append(p.daemon.calls, callProxyClose)

	return p.closeErr
}

type proxyStart struct {
	socketPath string
	upstream   string
}

// fakeProxyStarter records each start and hands back a fakeProxy, or fails
// with startErr.
func fakeProxyStarter(
	daemon *fakeDaemon,
	started *[]proxyStart,
	startErr, closeErr error,
) docker.ReadonlyProxyStarter {
	return func(
		_ context.Context,
		socketPath, upstreamSocketPath string,
	) (docker.ReadonlyProxy, error) {
		*started = append(*started, proxyStart{
			socketPath: socketPath,
			upstream:   upstreamSocketPath,
		})

		if startErr != nil {
			return nil, startErr
		}

		daemon.calls = append(daemon.calls, callProxyStart)

		return &fakeProxy{daemon: daemon, closeErr: closeErr}, nil
	}
}

func (d *fakeDaemon) PullImage(_ context.Context, reference string) error {
	d.pulled = append(d.pulled, reference)

	if d.pullErr != nil {
		return d.pullErr
	}

	d.absentUntilPulled = false

	return nil
}

func (d *fakeDaemon) InspectImage(
	_ context.Context,
	_ string,
) (docker.ImageIdentity, error) {
	if d.absentUntilPulled {
		return docker.ImageIdentity{}, commerr.ErrNotFound
	}

	return docker.ImageIdentity{
		ID:          testLocalImageID,
		RepoDigests: d.repoDigests,
	}, nil
}

func (d *fakeDaemon) StreamLogs(
	_ context.Context,
	containerID string,
) (io.ReadCloser, error) {
	d.loggedContainers = append(d.loggedContainers, containerID)

	if d.logErr != nil {
		return nil, d.logErr
	}

	return io.NopCloser(strings.NewReader(d.logPayload)), nil
}

func (d *fakeDaemon) CreateContainer(
	_ context.Context,
	request docker.CreateRequest,
) (string, error) {
	d.calls = append(d.calls, callCreate)

	if d.createErr != nil {
		return "", d.createErr
	}

	d.created = request

	return testContainerID, nil
}

func (d *fakeDaemon) AttachStdin(
	_ context.Context,
	_ string,
	document []byte,
) error {
	if d.attachErr != nil {
		return d.attachErr
	}

	d.stdin = document

	return nil
}

func (d *fakeDaemon) StartContainer(_ context.Context, id string) error {
	if d.startErr != nil {
		return d.startErr
	}

	d.started = append(d.started, id)
	d.running = true

	return nil
}

func (d *fakeDaemon) StopContainer(_ context.Context, id string) error {
	d.stopped = append(d.stopped, id)
	d.running = false

	return nil
}

func (d *fakeDaemon) RemoveContainer(_ context.Context, id string) error {
	d.calls = append(d.calls, callRemove)
	d.removed = append(d.removed, id)

	return nil
}

func (d *fakeDaemon) InspectContainer(
	_ context.Context,
	id string,
) (docker.ContainerState, error) {
	if d.staleName != "" && id == d.staleName && !slices.Contains(d.removed, id) {
		return docker.ContainerState{
			ID:      id,
			Running: d.staleRunning,
			Labels:  d.staleLabels,
		}, nil
	}

	// Only the container this fake created exists. A launch looks its name up
	// first, and a name nobody holds is not found.
	if id != testContainerID {
		return docker.ContainerState{}, commerr.ErrNotFound
	}

	if d.inspectErr != nil {
		return docker.ContainerState{}, d.inspectErr
	}

	return docker.ContainerState{
		ID:      id,
		Running: d.running,
		ImageID: testLocalImageID,
		Labels:  d.labels,
	}, nil
}
