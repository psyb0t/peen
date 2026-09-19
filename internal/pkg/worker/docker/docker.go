package docker

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/workerlog"
)

// Options builds a Docker worker launcher.
type Options struct {
	// Client is the daemon surface. A launcher without one cannot exist,
	// which is how the absence of Docker authority becomes a startup decision
	// rather than a per-launch surprise.
	Client Client

	// Identity is the host account every worker runs as.
	Identity HostIdentity

	// Image pins one immutable psyb0t/peen digest for every Docker profile.
	// Empty makes each profile name its own, which is equally pinned.
	Image string

	// DockerSocketGID is the host GID of the Docker socket, needed only by a
	// profile that mounts it into the worker.
	DockerSocketGID int

	// RuntimeEnvironment is the controller-selected configuration and named
	// provider credential set a Docker worker needs. It excludes controller
	// state and authority, and New validates it before any session launches.
	RuntimeEnvironment map[string]string
}

// Launcher starts session workers as psyb0t/peen containers.
type Launcher struct {
	client             Client
	identity           HostIdentity
	image              string
	dockerSocketGID    int
	runtimeEnvironment map[string]string
}

// New validates the launcher's dependencies.
//
// The host identity is checked here rather than at launch, so a deployment
// whose control account cannot own worker files fails at startup instead of on
// the first session.
func New(options Options) (*Launcher, error) {
	if options.Client == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"docker worker launcher client",
		)
	}

	if err := options.Identity.Validate(); err != nil {
		return nil, ctxerrors.Wrap(err, "validate the worker host identity")
	}

	if err := validateRuntimeEnvironment(
		options.RuntimeEnvironment,
	); err != nil {
		return nil, ctxerrors.Wrap(err, validateRuntimeEnvironmentOp)
	}

	return &Launcher{
		client:             options.Client,
		identity:           options.Identity,
		image:              options.Image,
		dockerSocketGID:    options.DockerSocketGID,
		runtimeEnvironment: maps.Clone(options.RuntimeEnvironment),
	}, nil
}

// Kind reports the profile kind this launcher serves.
func (l *Launcher) Kind() worker.Kind {
	return worker.KindDocker
}

// Launch creates, feeds, and starts one worker container.
//
// The launch document goes in over stdin after creation, so the credential is
// never part of the container's command, environment, labels, or image.
//
//nolint:ireturn // The Launcher contract is returning a Process.
func (l *Launcher) Launch(
	ctx context.Context,
	request worker.LaunchRequest,
) (worker.Process, error) {
	create, err := BuildCreateRequest(LaunchSpec{
		Document:           request.Document,
		Profile:            request.Profile,
		Identity:           l.identity,
		Image:              l.image,
		DockerSocketGID:    l.dockerSocketGID,
		RuntimeEnvironment: maps.Clone(l.runtimeEnvironment),
	})
	if err != nil {
		return nil, err
	}

	// The digest is resolved before anything is created. A launch that cannot
	// record what actually ran should not leave a container behind.
	imageDigest, err := l.resolveImageDigest(ctx, create.Image)
	if err != nil {
		return nil, err
	}

	document, err := request.Document.Encode()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode the worker launch document")
	}

	containerID, err := l.client.CreateContainer(ctx, create)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the worker container")
	}

	held := &process{
		client:      l.client,
		id:          containerID,
		sessionID:   request.Document.SessionID,
		imageDigest: imageDigest,
	}

	if err := l.client.AttachStdin(ctx, containerID, document); err != nil {
		return nil, l.abandon(ctx, held, err, "feed the worker launch document")
	}

	if err := l.client.StartContainer(ctx, containerID); err != nil {
		return nil, l.abandon(ctx, held, err, "start the worker container")
	}

	if _, err := l.client.InspectContainer(ctx, containerID); err != nil {
		return nil, l.abandon(ctx, held, err, "inspect the worker container")
	}

	l.startLogRelay(ctx, containerID, request.Document)
	logDockerWorkerStarted(ctx, request.Document, create, held.imageDigest)

	return held, nil
}

// logDockerWorkerStarted records the launch an operator needs to tie a running
// container back to the session and image digest it was started for.
func logDockerWorkerStarted(
	ctx context.Context,
	document worker.LaunchDocument,
	create CreateRequest,
	imageDigest string,
) {
	ctxscope.GetLogger(ctx).Info(
		"docker worker started",
		"session_id", document.SessionID.String(),
		"generation_id", document.GenerationID.String(),
		"container_name", create.Name,
		"image_digest", imageDigest,
		"network_disabled", create.NetworkDisabled,
	)
}

// startLogRelay carries the container's records into this process's logger.
//
// A Docker worker never receives the audit directory, so it cannot write the
// durable trail itself. Following its output here is what keeps hook, skill,
// child agent, and tool records in the controller's sinks. A failure to follow
// degrades the audit trail but must not fail an otherwise healthy worker, so it
// is logged rather than returned.
func (l *Launcher) startLogRelay(
	ctx context.Context,
	containerID string,
	document worker.LaunchDocument,
) {
	// The follow outlives this call, so it is deliberately detached from the
	// launch context, which ends as soon as the launch returns.
	relayCtx := context.WithoutCancel(ctx)

	stream, err := l.client.StreamLogs(relayCtx, containerID)
	if err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"following the worker container logs failed",
			"err", err,
			"reason", "audit_records_unavailable",
			"session_id", document.SessionID.String(),
			"container_id", containerID,
		)

		return
	}

	go func() {
		defer func() {
			if err := stream.Close(); err != nil {
				ctxscope.GetLogger(relayCtx).Debug(
					"closing the worker log stream failed",
					"err", err,
				)
			}
		}()

		workerlog.Relay(
			relayCtx,
			stream,
			document.SessionID,
			document.GenerationID,
		)
	}()
}

// inspectOrPull reads the image, fetching it first when the daemon does not
// already hold it.
//
// The Engine API's container create does not pull, unlike the docker CLI, which
// pulls on a 404 and retries. Without this a worker could not start on a host
// that had never seen the image.
func (l *Launcher) inspectOrPull(
	ctx context.Context,
	reference string,
) (ImageIdentity, error) {
	identity, err := l.client.InspectImage(ctx, reference)
	if err == nil {
		return identity, nil
	}

	if !errors.Is(err, commerr.ErrNotFound) {
		return ImageIdentity{}, ctxerrors.Wrap(err, "inspect the worker image")
	}

	ctxscope.GetLogger(ctx).Info(
		"pulling the worker image",
		"reason", "image_absent",
		"image", reference,
	)

	if err := l.client.PullImage(ctx, reference); err != nil {
		return ImageIdentity{}, ctxerrors.Wrap(err, "pull the worker image")
	}

	identity, err = l.client.InspectImage(ctx, reference)
	if err != nil {
		return ImageIdentity{}, ctxerrors.Wrap(
			err,
			"inspect the pulled worker image",
		)
	}

	return identity, nil
}

// resolveImageDigest asks the daemon which repository digest this image is.
//
// A container inspect reports a local image ID, which is not a digest, so it is
// never recorded as one. An image the daemon holds no repository digest for
// resolves to no digest rather than failing the launch.
func (l *Launcher) resolveImageDigest(
	ctx context.Context,
	reference string,
) (string, error) {
	identity, err := l.inspectOrPull(ctx, reference)
	if err != nil {
		return "", err
	}

	wanted := worker.ImageRepositoryOf(reference)

	for _, repoDigest := range identity.RepoDigests {
		if worker.ImageRepositoryOf(repoDigest) != wanted {
			continue
		}

		if worker.ImageDigestOf(repoDigest) == "" {
			continue
		}

		return repoDigest, nil
	}

	// An image built locally and never pushed has no repository digest. The
	// generation records none rather than the local image ID, because no
	// registry can resolve that ID later and storing it as a digest would be a
	// claim the image cannot back.
	return "", nil
}

// abandon removes a container that never became a usable worker, then reports
// the original failure.
func (l *Launcher) abandon(
	ctx context.Context,
	held *process,
	cause error,
	operation string,
) error {
	if err := held.Stop(context.WithoutCancel(ctx)); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"removing a failed worker container did not succeed",
			"reason", "worker_container_cleanup_failed",
			"container_id", held.id,
			"err", err,
		)
	}

	return ctxerrors.Wrap(cause, operation)
}

// process is one running worker container.
type process struct {
	client      Client
	id          string
	sessionID   uuid.UUID
	imageDigest string

	stopOnce sync.Once
	stopErr  error
	exitCode int
}

// Describe reports the container identity recorded with the generation.
func (p *process) Describe() worker.Descriptor {
	return worker.Descriptor{
		ContainerID: p.id,
		ImageDigest: p.imageDigest,
	}
}

// Stop ends and removes the container, but only after confirming the daemon
// still reports it as this controller's worker for this session.
//
// The stored container ID alone is not enough: an ID can be reused after a
// container is removed. Checking the labels too is what makes cleanup safe on a
// host running containers Peen does not own.
func (p *process) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() {
		p.stopErr = p.stop(ctx)
	})

	return p.stopErr
}

func (p *process) stop(ctx context.Context) error {
	state, err := p.client.InspectContainer(ctx, p.id)
	if err != nil {
		return ctxerrors.Wrap(err, "inspect the worker container before stop")
	}

	if !worker.OwnedBy(state.Labels, p.sessionID) {
		return ctxerrors.Wrapf(
			commerr.ErrPermissionDenied,
			"container %q is not this controller's worker for session %s",
			p.id,
			p.sessionID,
		)
	}

	if state.Running {
		if err := p.client.StopContainer(ctx, p.id); err != nil {
			return ctxerrors.Wrap(err, "stop the worker container")
		}
	}

	if final, err := p.client.InspectContainer(ctx, p.id); err == nil {
		p.exitCode = final.ExitCode
	}

	if err := p.client.RemoveContainer(ctx, p.id); err != nil {
		return ctxerrors.Wrap(err, "remove the worker container")
	}

	return nil
}

// Wait reports how the container ended.
func (p *process) Wait() (int, error) {
	return p.exitCode, nil
}

// socketDirectory is the one session directory a container must see to reach
// its controller.
//
// It is this session's own directory beneath the worker socket root, never the
// root itself. The root holds every live session's socket, so mounting it would
// show one worker where every other session's controller surface lives.
func socketDirectory(socketPath string) string {
	return filepath.Dir(socketPath)
}
