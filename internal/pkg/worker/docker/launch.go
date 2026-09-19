package docker

import (
	"maps"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

const (
	// dockerSocketPath is the host socket a host-like profile may mount into
	// the worker. It keeps its literal path inside the worker, like every
	// other mount. This grant is separate from the controller's own socket:
	// mounting it here is what makes a worker host-root-equivalent.
	dockerSocketPath = "/var/run/docker.sock"

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
	Document worker.LaunchDocument
	Profile  worker.Profile
	Identity HostIdentity

	// RuntimeEnvironment is the controller-selected subset of its deployment
	// environment a worker needs to build the same agent runtime. It includes
	// provider credentials by their operator-chosen names, never the private
	// worker launch credential. Controller-only keys are rejected below.
	RuntimeEnvironment map[string]string

	// Image overrides the profile's image. A deployment that pins one
	// release-matched psyb0t/peen image sets it once rather than repeating it
	// in every profile.
	Image string

	// DockerSocketGID is the host GID of the Docker socket. It is required
	// when the profile mounts that socket, because the worker account needs
	// the group to use it.
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

	if spec.Profile.AllowPrivilegeEscalation {
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
		NetworkDisabled:  !spec.Profile.AllowNetwork,
		NoNewPrivileges:  !spec.Profile.AllowPrivilegeEscalation,
		Labels:           worker.ContainerLabels(spec.Document.SessionID),
	}, nil
}

func (s LaunchSpec) image() string {
	if s.Image != "" {
		return s.Image
	}

	return s.Profile.Image
}

func (s LaunchSpec) validate() error {
	if err := s.Document.Validate(); err != nil {
		return ctxerrors.Wrap(err, "validate the worker launch document")
	}

	if s.Profile.Kind != worker.KindDocker {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"execution profile %q is not a Docker profile",
			s.Profile.Name,
		)
	}

	if err := s.Profile.Validate(); err != nil {
		return ctxerrors.Wrap(err, "validate the worker execution profile")
	}

	if err := validateRuntimeEnvironment(s.RuntimeEnvironment); err != nil {
		return ctxerrors.Wrap(err, validateRuntimeEnvironmentOp)
	}

	if s.image() == "" {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"execution profile %q names no worker image",
			s.Profile.Name,
		)
	}

	// A container cannot be created without an image. Nothing else about the
	// reference is checked here: the daemon is the authority on whether it can
	// be resolved, and an operator naming their own image is naming it on
	// purpose.
	if s.image() == "" {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"execution profile %q has no worker image",
			s.Profile.Name,
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
		"PEEN_EXECUTION_PROFILES",
		"PEEN_DEFAULT_EXECUTION_PROFILE",
		"PEEN_WORKSPACE_ROOTS",
		"PEEN_HTTP_LISTEN_ADDRESS",
		"PEEN_METRICS_LISTEN_ADDRESS",
		"PEEN_HOST_USERNAME",
		"PEEN_HOST_HOME",
		"PEEN_LOG_DIRECTORY",
		"PEEN_LOG_RETENTION_DAYS",
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
// Extra mounts come from the profile. The Docker socket is added only when the
// named profile allows it, never by default.
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

	mounts = append(mounts, s.Profile.Resolved()...)

	groups := s.Identity.Groups()

	if !s.Profile.AllowDockerSocket {
		return mounts, groups, nil
	}

	if s.DockerSocketGID <= rootUID {
		return nil, nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"execution profile %q mounts the Docker socket but no socket "+
				"group was discovered",
			s.Profile.Name,
		)
	}

	mounts = append(mounts, worker.Mount{
		Source: dockerSocketPath,
		Target: dockerSocketPath,
	})

	groups = append(groups, HostIdentity{
		SupplementaryGIDs: []int{s.DockerSocketGID},
	}.Groups()...)

	return mounts, groups, nil
}
