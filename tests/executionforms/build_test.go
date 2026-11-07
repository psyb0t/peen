//go:build integration

package executionforms

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type executionForm struct {
	name  string
	build func(t *testing.T, repository string) string
}

func buildSourceForm(t *testing.T, repository string) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), executionFormsSourceBinaryName)
	runBuildCommand(t, repository, nil, "go", "build", "-o", binary, executionFormsCommandPackage)

	return binary
}

func buildInstalledForm(t *testing.T, repository string) string {
	t.Helper()

	installDirectory := t.TempDir()
	runBuildCommand(
		t,
		repository,
		[]string{"GOBIN=" + installDirectory},
		"go",
		"install",
		executionFormsCommandPackage,
	)

	return filepath.Join(installDirectory, executionFormsInstalledBinaryName)
}

func runBuildCommand(
	t *testing.T,
	directory string,
	environment []string,
	name string,
	arguments ...string,
) {
	t.Helper()

	command := exec.CommandContext(t.Context(), name, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func executionFormsRepositoryRoot(t *testing.T) string {
	t.Helper()

	directory, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(
			filepath.Join(directory, executionFormsRepositoryModuleFile),
		); statErr == nil {
			return directory
		}

		parent := filepath.Dir(directory)
		if parent == directory {
			require.Fail(t, "repository root not found")

			return ""
		}
		directory = parent
	}
}
