//go:build dockerworker

package dockerworker_test

import (
	"time"

	"github.com/psyb0t/peen/internal/pkg/worker/dockerproxy"
)

const (
	// enableEnvKey is the explicit opt-in. The build tag alone is not enough: a
	// target that compiles this package should still refuse to create a
	// container unless the operator asked for one in this run.
	enableEnvKey = "PEEN_DOCKER_WORKER_TEST"

	// imageEnvKey names the worker image to probe. There is no default, because
	// a default would let this suite create a container from an unrequested
	// local daemon image.
	imageEnvKey = "PEEN_DOCKER_WORKER_TEST_IMAGE"

	// localImageEnvKey is set only by make test-docker-worker-source. The
	// profile uses a local tag to prove the normal builder accepts it, then the
	// test swaps the probe request to the Dockerfile image it built itself.
	localImageEnvKey = "PEEN_DOCKER_WORKER_TEST_LOCAL_IMAGE"

	// socketEnvKey names the daemon socket. The controller's own socket is a
	// deliberate grant, so it is named rather than discovered.
	socketEnvKey = "PEEN_DOCKER_WORKER_TEST_SOCKET"

	enabledValue = "true"

	directoryMode = 0o700
	fileMode      = 0o600

	testProfileName  = "audit-sandbox"
	repositoryModule = "go.mod"
	testingDirectory = ".testing"
	// rootPrefix, rootSuffixLength, and socketRootName are short because a
	// session socket under this root must fit the 107-byte Unix socket
	// address, and the root sits inside the checkout's own path.
	rootPrefix       = "dw-"
	rootSuffixLength = 8

	dockerSocketPath = "/var/run/docker.sock"
	agentsFileName   = "AGENTS.md"
	agentsFileBody   = "Follow the entrypoint contract test rules.\n"

	// stateSecretName stands in for anything controller-owned under the state
	// directory. The state directory is never mounted, so the probe must not
	// find it.
	stateSecretName = "controller-only-secret"
	stateSecretBody = "this must never be readable from a worker\n"

	socketRootName    = "w"
	socketSentinel    = "worker.sock"
	resultFileName    = "probe-result.json"
	ownedFileName     = "probe-owned-file"
	probeShell        = "/bin/bash"
	probeShellFlag    = "-c"
	tiniPath          = "/usr/bin/tini"
	tiniSeparator     = "--"
	peenEntrypoint    = "/usr/local/bin/peen-entrypoint"
	expectedRootUsers = "0:0"
	sourceImagePrefix = "peen-dockerworker-source-"

	// sourceContractProfileImage is a local image tag. The source-image target
	// never asks Docker to run it. It proves the normal builder accepts a local
	// tag, then replaces only the test probe's image with the just-built image.
	sourceContractProfileImage = "peen-dockerworker-contract:local"

	// containerExitTimeout bounds the probe. It writes one file and exits, so a
	// container still running after this is a finding rather than slowness.
	containerExitTimeout = 90 * time.Second
	containerPollEvery   = 250 * time.Millisecond
	teardownTimeout      = 60 * time.Second

	probeResultEnvKey      = "PEEN_PROBE_RESULT"
	probeOwnedFileEnvKey   = "PEEN_PROBE_OWNED_FILE"
	probeStateSecretEnvKey = "PEEN_PROBE_STATE_SECRET" //nolint:gosec // An environment variable name, not a credential.
	probeForeignSockEnvKey = "PEEN_PROBE_FOREIGN_SOCKET"
	probeSessionDirEnvKey  = "PEEN_PROBE_SESSION_SOCKET_DIR"
	probeSocketRootEnvKey  = "PEEN_PROBE_SOCKET_ROOT"
	probeLearnedDirEnvKey  = "PEEN_PROBE_LEARNED_DIR"
	probeLearnedRuleEnvKey = "PEEN_PROBE_LEARNED_RULE"

	// learnedDirectoryPath is the global learned directory's default place
	// inside the config directory, mounted on top of the config mount.
	learnedDirectoryPath = ".agents/learned"
	learnedRulePath      = "rules/learned.md"
	learnedRuleBody      = "A rule the agent learned.\n"

	probeListBodyEnvKey    = "PEEN_PROBE_LIST_BODY"
	probeInspectBodyEnvKey = "PEEN_PROBE_INSPECT_BODY"
	probeCreateBodyEnvKey  = "PEEN_PROBE_CREATE_BODY"

	listBodyFileName    = "probe-list.json"
	inspectBodyFileName = "probe-inspect.json"
	createBodyFileName  = "probe-create.json"
	httpStatusOK        = "200"
	httpStatusForbidden = "403"
	dockerHostEnvKey    = "DOCKER_HOST"
	dockerHostScheme    = "unix://"
	redactedEnvSuffix   = "=" + dockerproxy.RedactedValue
)
