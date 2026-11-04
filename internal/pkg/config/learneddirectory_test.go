package config_test

import (
	"path/filepath"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const learnedTestCustomDirectory = "/srv/peen/learned"

// The global learned directory defaults to the config layer's own
// .agents/learned, and Docker workers get it only while auto-evolve is on.
func TestLearnedDirectoryDefaultsAndWorkerGrant(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		autoEvolve  bool
		learned     string
		wantLearned string
		wantWorker  string
	}{
		{
			name: "default with auto-evolve off",
			wantLearned: filepath.Join(
				stateTestConfigDirectory,
				".agents",
				"learned",
			),
		},
		{
			name:       "default with auto-evolve on",
			autoEvolve: true,
			wantLearned: filepath.Join(
				stateTestConfigDirectory,
				".agents",
				"learned",
			),
			wantWorker: filepath.Join(
				stateTestConfigDirectory,
				".agents",
				"learned",
			),
		},
		{
			name:        "custom directory with auto-evolve on",
			autoEvolve:  true,
			learned:     learnedTestCustomDirectory + "/",
			wantLearned: learnedTestCustomDirectory,
			wantWorker:  learnedTestCustomDirectory,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			deployment := stateTestConfig()
			deployment.AutoEvolve = tc.autoEvolve
			deployment.LearnedDirectory = tc.learned

			require.NoError(t, deployment.Validate())
			assert.Equal(t, tc.wantLearned, deployment.LearnedDir())
			assert.Equal(t, tc.wantWorker, deployment.WorkerLearnedDirectory())
		})
	}
}

// Workers write the global learned directory only when auto-evolve is on and
// the operator opted in, because every session loads what one session saves
// there.
func TestLearnedDirectoryWritable(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		autoEvolve      bool
		writableLearned bool
		writableConfig  bool
		want            bool
	}{
		{name: "auto-evolve alone", autoEvolve: true},
		{name: "opt-in without auto-evolve", writableLearned: true},
		{
			name:            "learned directory opt-in",
			autoEvolve:      true,
			writableLearned: true,
			want:            true,
		},
		{
			name:           "config directory opt-in",
			autoEvolve:     true,
			writableConfig: true,
			want:           true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			deployment := stateTestConfig()
			deployment.AutoEvolve = tc.autoEvolve
			deployment.WritableLearnedDirectory = tc.writableLearned
			deployment.WritableConfigDirectory = tc.writableConfig

			assert.Equal(t, tc.want, deployment.LearnedDirectoryWritable())
		})
	}
}

// A Docker worker mounts the learned directory writable, so it may not be or
// contain the read-only config directory, and may not overlap controller
// state or the worker sockets.
func TestConfigRefusesALearnedDirectoryOverPrivateDirectories(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		learned string
	}{
		{name: "relative", learned: "learned"},
		{name: "the config directory", learned: stateTestConfigDirectory},
		{name: "containing the config directory", learned: "/srv/peen"},
		{name: "the state directory", learned: stateTestStateDirectory},
		{
			name:    "inside the state directory",
			learned: filepath.Join(stateTestStateDirectory, "learned"),
		},
		{
			name: "inside the worker socket root",
			learned: filepath.Join(
				stateTestConfig().WorkerSocketRoot(),
				"learned",
			),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			deployment := stateTestConfig()
			deployment.AutoEvolve = true
			deployment.LearnedDirectory = tc.learned

			require.ErrorIs(t, deployment.Validate(), config.ErrInvalidConfig)
		})
	}
}

func TestPathsOverlap(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		first  string
		second string
		want   bool
	}{
		{name: "same", first: "/a/b", second: "/a/b/", want: true},
		{name: "first contains second", first: "/a", second: "/a/b", want: true},
		{name: "second contains first", first: "/a/b", second: "/a", want: true},
		{name: "siblings sharing a prefix", first: "/a/b", second: "/a/bc"},
		{name: "root contains everything", first: "/", second: "/a", want: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, config.PathsOverlap(tc.first, tc.second))
		})
	}
}
