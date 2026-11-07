//go:build dockerworker

package dockerworker_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/require"
)

// disposableRoot is one run's entire filesystem footprint, all of it inside the
// repository so a bind source resolves to the same bytes on host and daemon.
type disposableRoot struct {
	root        string
	config      string
	learned     string
	state       string
	workspace   string
	socketRoot  string
	stateSecret string

	// foreignSocketPath is a second session's socket under the shared root. The
	// probe must not be able to see it.
	foreignSocketPath string
}

// newDisposableRoot builds this run's whole filesystem footprint inside the
// repository's gitignored .testing/ directory.
//
// It is not t.TempDir() and not os.MkdirTemp(""). Those land in the test
// container's /tmp, which the host daemon would resolve somewhere else
// entirely, and every mount assertion in this file would then be inspecting a
// daemon-created empty directory rather than the one the test wrote to.
func newDisposableRoot(t *testing.T) disposableRoot {
	t.Helper()

	root := filepath.Join(
		repositoryPath(t, testingDirectory),
		rootPrefix+uuid.NewString()[:rootSuffixLength],
	)

	paths := disposableRoot{
		root:      root,
		config:    filepath.Join(root, "config"),
		state:     filepath.Join(root, "state"),
		workspace: filepath.Join(root, "workspace"),
	}
	paths.socketRoot = filepath.Join(root, socketRootName)
	paths.stateSecret = filepath.Join(paths.state, stateSecretName)
	paths.learned = filepath.Join(paths.config, learnedDirectoryPath)

	for _, directory := range []string{
		paths.config,
		paths.state,
		paths.workspace,
		paths.socketRoot,
		filepath.Dir(paths.learnedRule()),
	} {
		require.NoError(t, os.MkdirAll(directory, directoryMode))
	}

	require.NoError(t, os.WriteFile(
		filepath.Join(paths.config, agentsFileName),
		[]byte(agentsFileBody),
		fileMode,
	))

	require.NoError(t, os.WriteFile(
		paths.learnedRule(),
		[]byte(learnedRuleBody),
		fileMode,
	))

	// The probe must not be able to read this. It stands in for peen.db and the
	// audit logs, which live in the same directory.
	require.NoError(t, os.WriteFile(
		paths.stateSecret,
		[]byte(stateSecretBody),
		fileMode,
	))

	// Only the root this test created is removed.
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })

	return paths
}

// writeForeignSocketSentinel plants a second session's socket directory under
// the shared socket root. The probe must not be able to see it, which is what
// proves a worker receives its own directory rather than the root.
func (d *disposableRoot) writeForeignSocketSentinel(
	t *testing.T,
	foreignSessionID uuid.UUID,
) {
	t.Helper()

	directory := worker.SessionSocketDirectory(d.socketRoot, foreignSessionID)
	require.NoError(t, os.MkdirAll(directory, directoryMode))

	sentinel := filepath.Join(directory, socketSentinel)
	require.NoError(t, os.WriteFile(sentinel, nil, fileMode))

	d.foreignSocketPath = sentinel
}

func (d disposableRoot) learnedRule() string {
	return filepath.Join(d.learned, learnedRulePath)
}

func (d disposableRoot) resultPath() string {
	return filepath.Join(d.workspace, resultFileName)
}

func (d disposableRoot) ownedFilePath() string {
	return filepath.Join(d.workspace, ownedFileName)
}

func (d disposableRoot) readProbeResult(t *testing.T) probeResult {
	t.Helper()

	raw, err := os.ReadFile(d.resultPath())
	require.NoError(t, err, "the probe wrote no result file")

	result := probeResult{}
	require.NoError(t, json.Unmarshal(raw, &result))

	return result
}

func (d disposableRoot) launchDocument(
	t *testing.T,
	sessionID uuid.UUID,
) worker.LaunchDocument {
	t.Helper()

	credential, err := worker.NewCredential()
	require.NoError(t, err)

	document := worker.LaunchDocument{
		SessionID:       sessionID,
		GenerationID:    uuid.New(),
		Credential:      credential,
		SocketPath:      worker.SocketPathFor(d.socketRoot, sessionID),
		Workspace:       d.workspace,
		ConfigDirectory: d.config,
		AgentSpace:      worker.DockerSpaceName(testProfileName),
	}
	require.NoError(t, document.Validate())

	// The session's own socket directory has to exist before the container
	// mounts it, and it is the only part of the socket root the worker sees.
	require.NoError(t, os.MkdirAll(
		worker.SessionSocketDirectory(d.socketRoot, sessionID),
		directoryMode,
	))

	return document
}
