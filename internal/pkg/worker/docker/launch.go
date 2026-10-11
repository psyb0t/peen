package docker

import (
	"maps"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

const (
	// workerDockerSocketPath is where a `docker: full` profile's socket
	// appears inside the worker, which is where Docker clients look. The
	// source is the socket the controller resolved. Mounting it is what makes
	// a worker host root.
	workerDockerSocketPath = "/var/run/docker.sock"

	// configEnvKey points the worker at the same global configuration the
	// controller reads, mounted read-only at its literal host path.
	configEnvKey = "PEEN_CONFIG_DIR"

	// Every Docker worker begins as root only long enough for the image
	// entrypoint to reconcile this account and drop to it. They name the
	// account and whether that account gets sudo.
	bootstrapUIDEnvKey       = "PEEN_WORKER_UID"
	bootstrapGIDEnvKey       = "PEEN_WORKER_GID"
	bootstrapUsernameEnvKey  = "PEEN_WORKER_USERNAME"
	bootstrapHomeEnvKey      = "PEEN_WORKER_HOME"
	bootstrapGroupsEnvKey    = "PEEN_WORKER_SUPPLEMENTARY_GIDS"
	bootstrapAllowSudoEnvKey = "PEEN_WORKER_ALLOW_SUDO"
	bootstrapAllowSudoValue  = "true"

	workerEnvironmentBaseEntries = 8
	validateRuntimeEnvironmentOp = "validate Docker worker runtime environment"

	// bootstrapUser starts a worker as root so the image entrypoint can create
	// or reconcile the controller's host account, then drop to it before the
	// agent runs. Root is bootstrap only and never the agent.
	bootstrapUser = "0:0"
)

// LaunchSpec is everything needed to build one worker container.
type LaunchSpec struct {
	Document   worker.LaunchDocument
	AgentSpace worker.AgentSpace
	Identity   HostIdentity

	// RuntimeEnvironment is the controller-selected subset of its deployment
	// environment a worker needs to build the same agent runtime. It includes
	// provider credentials by their operator-chosen names, never the private
	// worker launch credential. Controller-only keys are rejected below.
	RuntimeEnvironment map[string]string

	// ImageOverride replaces every agent space's image. A deployment that pins
	// one release-matched image sets it once rather than repeating it in every
	// profile.
	ImageOverride string

	// ImageFallback is the image a worker runs when there is no override and
	// the agent space names none. It must never take precedence over a
	// profile's own image.
	ImageFallback string

	// DockerSocketPath is the host socket the controller resolved. A
	// `docker: full` agent space mounts it at the conventional path inside
	// the worker.
	DockerSocketPath string

	// DockerSocketGID is the host GID of the Docker socket. It is required
	// when the agent space mounts that socket, because the worker account
	// needs the group to use it.
	DockerSocketGID int
}

// BuildCreateRequest turns operator configuration and the controller's host
// identity into one container launch.
//
// Paths are literal. The workspace, the global configuration, the worker socket
// root, and every extra mount appear inside the worker at the same absolute
// path they have on the host, so paths in prompts, hooks, logs, and tool calls
// stay truthful. The only relocation is one an operator wrote as a mount
// target.
//
// The launch document is not built into the container. It goes in over stdin
// after creation, so the credential never appears in the command line, the
// environment, or any image layer.
func BuildCreateRequest(spec LaunchSpec) (CreateRequest, error) {
	if err := spec.validate(); err != nil {
		return CreateRequest{}, err
	}

	mounts, groups, err := spec.buildMounts()
	if err != nil {
		return CreateRequest{}, err
	}

	environment := make(
		map[string]string,
		len(spec.RuntimeEnvironment)+workerEnvironmentBaseEntries,
	)
	maps.Copy(environment, spec.RuntimeEnvironment)
	maps.Copy(environment, spec.Identity.Environment())
	environment[configEnvKey] = spec.Document.ConfigDirectory

	maps.Copy(environment, spec.Identity.BootstrapEnvironment(groups))

	if spec.AgentSpace.Sudo {
		environment[bootstrapAllowSudoEnvKey] = bootstrapAllowSudoValue
	}

	return CreateRequest{
		Name:             worker.ContainerName(spec.Document.SessionID),
		Image:            spec.image(),
		Command:          []string{worker.WorkerCommand},
		Mounts:           mounts,
		User:             bootstrapUser,
		Groups:           groups,
		Env:              environment,
		WorkingDirectory: spec.Document.Workspace,
		NetworkMode:      spec.AgentSpace.Network,
		NoNewPrivileges:  !spec.AgentSpace.Sudo,
		Labels:           worker.ContainerLabels(spec.Document.SessionID),
	}, nil
}

// ValidateFullAccessSocket refuses, at startup, a `docker: full` agent space
// whose socket group the worker account cannot be given. A socket owned by
// group 0 is such a case, and finding it at the first launch would leave a
// controller that reports ready and cannot start that worker.
func ValidateFullAccessSocket(
	agentSpaces *worker.AgentSpaceSet,
	socketGID int,
) error {
	if socketGID > rootUID {
		return nil
	}

	for _, name := range agentSpaces.Names() {
		space, err := agentSpaces.Select(name)
		if err != nil {
			return ctxerrors.Wrapf(err, "select agent space %q", name)
		}

		if space.Docker != worker.DockerAccessFull {
			continue
		}

		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"agent space %q mounts the Docker socket, but the socket's "+
				"group is %d and cannot be granted to the worker account",
			name,
			socketGID,
		)
	}

	return nil
}

// image picks the worker image: the deployment override, else the agent
// space's own image, else the fallback.
func (s LaunchSpec) image() string {
	switch {
	case s.ImageOverride != "":
		return s.ImageOverride
	case s.AgentSpace.Image != "":
		return s.AgentSpace.Image
	default:
		return s.ImageFallback
	}
}

func (s LaunchSpec) validate() error {
	if err := s.Document.Validate(); err != nil {
		return ctxerrors.Wrap(err, "validate the worker launch document")
	}

	if s.AgentSpace.Kind != worker.KindDocker {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"agent space %q is not a Docker agent space",
			s.AgentSpace.Name,
		)
	}

	if err := s.AgentSpace.Validate(); err != nil {
		return ctxerrors.Wrap(err, "validate the worker agent space")
	}

	if err := validateRuntimeEnvironment(s.RuntimeEnvironment); err != nil {
		return ctxerrors.Wrap(err, validateRuntimeEnvironmentOp)
	}

	// A container cannot be created without an image. Nothing else about the
	// reference is checked here: the daemon is the authority on whether it can
	// be resolved, and an operator naming their own image is naming it on
	// purpose.
	if s.image() == "" {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"agent space %q names no worker image",
			s.AgentSpace.Name,
		)
	}

	return s.Identity.Validate()
}

// validateRuntimeEnvironment is defense in depth for the config allowlist.
// BuildCreateRequest is the final boundary before the daemon sees a worker
// environment, so a mistaken caller cannot smuggle controller state, the
// public API token, or the entrypoint's privilege-drop controls into a worker.
func validateRuntimeEnvironment(environment map[string]string) error {
	for key := range environment {
		if key == "" || strings.Contains(key, "=") {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"invalid worker environment key %q",
				key,
			)
		}

		if workerEnvironmentReserved(key) {
			return ctxerrors.Wrapf(
				commerr.ErrPermissionDenied,
				"worker environment key %q is controller-owned",
				key,
			)
		}
	}

	return nil
}

func workerEnvironmentReserved(key string) bool {
	if strings.HasPrefix(key, "PEEN_WORKER_") {
		return true
	}

	switch key {
	case configEnvKey,
		"PEEN_STATE_DIR",
		"PEEN_API_TOKEN",
		"PEEN_DOCKER_SOCKET",
		"PEEN_WORKER_IMAGE",
		"PEEN_DOCKER_PROFILES",
		"PEEN_DEFAULT_AGENT_SPACE",
		"PEEN_WORKSPACE_ROOT",
		"PEEN_HTTP_LISTEN_ADDRESS",
		"PEEN_METRICS_LISTEN_ADDRESS",
		"PEEN_HOST_USERNAME",
		"PEEN_HOST_HOME",
		"PEEN_LOG_DIRECTORY",
		"PEEN_LOG_RETENTION",
		"DOCKER_HOST",
		"HOME",
		"USER",
		"LOGNAME":
		return true
	}

	return false
}

// buildMounts assembles the worker's mounts and the groups it needs for them.
//
// The workspace is writable, the global configuration is read-only, and the
// worker's own socket directory is writable so it can reach its controller.
// Extra mounts come from the Docker profile. The Docker socket is added only
// when the profile says `docker: full`, never by default.
func (s LaunchSpec) buildMounts() ([]worker.Mount, []string, error) {
	mounts := []worker.Mount{
		{
			Source: s.Document.Workspace,
			Target: s.Document.Workspace,
		},
		{
			Source:   s.Document.ConfigDirectory,
			Target:   s.Document.ConfigDirectory,
			ReadOnly: true,
		},
		{
			Source: socketDirectory(s.Document.SocketPath),
			Target: socketDirectory(s.Document.SocketPath),
		},
	}

	mounts = append(mounts, s.AgentSpace.Resolved()...)

	groups := s.Identity.Groups()

	if s.AgentSpace.Docker != worker.DockerAccessFull {
		return mounts, groups, nil
	}

	if s.DockerSocketGID <= rootUID {
		return nil, nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"agent space %q mounts the Docker socket but no socket "+
				"group was discovered",
			s.AgentSpace.Name,
		)
	}

	if s.DockerSocketPath == "" {
		return nil, nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"agent space %q mounts the Docker socket but the controller "+
				"resolved no socket path",
			s.AgentSpace.Name,
		)
	}

	mounts = append(mounts, worker.Mount{
		Source: s.DockerSocketPath,
		Target: workerDockerSocketPath,
	})

	groups = append(groups, HostIdentity{
		SupplementaryGIDs: []int{s.DockerSocketGID},
	}.Groups()...)

	return mounts, groups, nil
}
