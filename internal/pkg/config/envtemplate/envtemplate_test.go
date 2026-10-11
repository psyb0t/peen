package envtemplate

import (
	"os"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// committedExamplePath is .env.example at the repository root, relative to
// this package directory, where go test runs.
const committedExamplePath = "../../../../.env.example"

// The committed .env.example is this template rendered with no values. A
// template edit without `make generate` fails here.
func TestRenderWithNoValuesMatchesTheCommittedExample(t *testing.T) {
	t.Parallel()

	committed, err := os.ReadFile(committedExamplePath)
	require.NoError(t, err)

	rendered, err := Render(Values{})
	require.NoError(t, err)

	assert.Equal(t, string(committed), string(rendered))
}

func TestRenderWritesEveryGivenValue(t *testing.T) {
	t.Parallel()

	rendered, err := Render(fullValues())
	require.NoError(t, err)

	lines := strings.Split(string(rendered), "\n")

	wantLines := []string{
		"PEEN_CONFIG_DIR=/home/someone/.config/peen/config",
		"PEEN_STATE_DIR=/home/someone/.config/peen/state",
		"PEEN_WORKSPACE_ROOT=/home/someone",
		"PEEN_DEFAULT_AGENT_SPACE=docker:default",
		"PEEN_WORKER_IMAGE=example.invalid/peen:v0.0.0",
		"PEEN_HTTP_LISTEN_ADDRESS=127.0.0.1:8080",
		"PEEN_HOST_USERNAME=someone",
		"PEEN_HOST_HOME=/home/someone",
		"PEEN_CONTROL_SPACE=docker",
		"PEEN_IMAGE=example.invalid/peen:v0.0.0",
		"PEEN_CONTAINER_NAME=peen-someone",
	}

	// Each given key is set exactly once and never left as a commented
	// placeholder.
	for _, want := range wantLines {
		key, _, _ := strings.Cut(want, "=")

		assert.Contains(t, lines, want)
		assert.Equal(
			t,
			1,
			countLinesWithPrefix(lines, key+"="),
			"setting lines for %s",
			key,
		)
		assert.Zero(
			t,
			countLinesWithPrefix(lines, "# "+key+"="),
			"placeholder lines for %s",
			key,
		)
	}
}

func TestRenderWithNoValuesKeepsPlaceholdersAndDefaults(t *testing.T) {
	t.Parallel()

	rendered, err := Render(Values{})
	require.NoError(t, err)

	lines := strings.Split(string(rendered), "\n")

	for _, want := range []string{
		"PEEN_CONFIG_DIR=/absolute/path/to/peen/config",
		"PEEN_STATE_DIR=/absolute/path/to/peen/state",
		"# PEEN_WORKSPACE_ROOT=/absolute/path/to/projects",
		"# PEEN_DEFAULT_AGENT_SPACE=docker:ops",
		"# PEEN_WORKER_IMAGE=psyb0t/peen:vX.Y.Z",
		"PEEN_HTTP_LISTEN_ADDRESS=:8080",
		"# PEEN_HOST_USERNAME=your-host-user",
		"# PEEN_HOST_HOME=/home/your-host-user",
		"# PEEN_CONTROL_SPACE=docker",
		"# PEEN_IMAGE=psyb0t/peen:vX.Y.Z",
		"# PEEN_CONTAINER_NAME=peen-your-host-user",
	} {
		assert.Contains(t, lines, want)
	}
}

func TestRenderAcceptsEveryAgentAndControlSpace(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		values Values
	}{
		{
			name:   "control agent space",
			values: Values{DefaultAgentSpace: "control"},
		},
		{
			name:   "named profile",
			values: Values{DefaultAgentSpace: "docker:ops-1.a_b"},
		},
		{
			name:   "host control space",
			values: Values{ControlSpace: ControlSpaceHost},
		},
		{
			name:   "docker control space",
			values: Values{ControlSpace: ControlSpaceDocker},
		},
		{
			name:   "dotted container name",
			values: Values{ContainerName: "peen-a.b_c"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Render(tc.values)
			require.NoError(t, err)
		})
	}
}

func TestRenderRefusesInvalidValues(t *testing.T) {
	t.Parallel()

	for _, tc := range invalidValueCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rendered, err := Render(tc.values)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assert.Nil(t, rendered)
		})
	}
}

// invalidValueCase is one set of values Render and WriteNew must refuse.
type invalidValueCase struct {
	name   string
	values Values
}

func invalidValueCases() []invalidValueCase {
	return []invalidValueCase{
		{name: "line feed", values: Values{Image: "a\nPEEN_API_TOKEN=x"}},
		{name: "carriage return", values: Values{HostUsername: "a\rb"}},
		{
			name:   "relative config dir",
			values: Values{ConfigDirectory: "config"},
		},
		{
			name:   "relative state dir",
			values: Values{StateDirectory: "./state"},
		},
		{
			name:   "relative workspace root",
			values: Values{WorkspaceRoot: "~"},
		},
		{name: "relative host home", values: Values{HostHome: "home/a"}},
		{
			name:   "unknown agent space",
			values: Values{DefaultAgentSpace: "host"},
		},
		{name: "empty profile", values: Values{DefaultAgentSpace: "docker:"}},
		{
			name:   "profile starting with a hyphen",
			values: Values{DefaultAgentSpace: "docker:-x"},
		},
		{
			name:   "profile with a slash",
			values: Values{DefaultAgentSpace: "docker:a/b"},
		},
		{name: "bad control space", values: Values{ControlSpace: "control"}},
		{
			name:   "container name starting with a hyphen",
			values: Values{ContainerName: "-peen"},
		},
		{
			name:   "container name with a slash",
			values: Values{ContainerName: "peen/a"},
		},
		{
			name:   "container name with a space",
			values: Values{ContainerName: "peen a"},
		},
	}
}

func fullValues() Values {
	return Values{
		ConfigDirectory:   "/home/someone/.config/peen/config",
		StateDirectory:    "/home/someone/.config/peen/state",
		WorkspaceRoot:     "/home/someone",
		DefaultAgentSpace: "docker:default",
		WorkerImage:       "example.invalid/peen:v0.0.0",
		HTTPListenAddress: "127.0.0.1:8080",
		HostUsername:      "someone",
		HostHome:          "/home/someone",
		ControlSpace:      ControlSpaceDocker,
		Image:             "example.invalid/peen:v0.0.0",
		ContainerName:     "peen-someone",
	}
}

func countLinesWithPrefix(lines []string, prefix string) int {
	count := 0

	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}

	return count
}
