package worker_test

import (
	"testing"

	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
)

const (
	testDigest = "sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
	testOtherDigest = "sha256:" +
		"2222222222222222222222222222222222222222222222222222222222222222"
)

// The repository is what a reported repository digest is matched against, so a
// digest for some other repository is never recorded as this worker's image.
func TestImageRepositoryOf(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		reference string
		want      string
	}{
		{
			name:      "bare repository",
			reference: "psyb0t/peen",
			want:      "psyb0t/peen",
		},
		{
			name:      "digest reference",
			reference: "psyb0t/peen@" + testDigest,
			want:      "psyb0t/peen",
		},
		{
			name:      "tagged reference",
			reference: "psyb0t/peen:v1.2.3",
			want:      "psyb0t/peen",
		},
		{
			name:      "Docker Hub host prefix",
			reference: "index.docker.io/psyb0t/peen@" + testDigest,
			want:      "psyb0t/peen",
		},
		{
			name:      "registry host with a port keeps its host",
			reference: "registry.example:5000/psyb0t/peen@" + testDigest,
			want:      "registry.example:5000/psyb0t/peen",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, worker.ImageRepositoryOf(tc.reference))
		})
	}
}

func TestImageDigestOf(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		testDigest,
		worker.ImageDigestOf("psyb0t/peen@"+testDigest),
	)
	assert.Empty(t, worker.ImageDigestOf("psyb0t/peen:latest"))
}

// The worker image tracks the controller's own build, which is what lets a
// release run the worker published beside it without an operator writing a
// digest that does not exist until after the push.
func TestImageForBuildVersion(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		version string
		want    string
	}{
		{
			name:    "release tag",
			version: "v1.2.3",
			want:    worker.ImageRepository + ":v1.2.3",
		},
		{
			// The version is used verbatim. A build with no release tag on HEAD
			// names an image the registry has no reason to hold, which is what
			// PEEN_WORKER_IMAGE is for locally.
			name:    "untagged build uses its version verbatim",
			version: "dev",
			want:    worker.ImageRepository + ":dev",
		},
		{
			name:    "surrounding whitespace",
			version: "  v9.9.9  ",
			want:    worker.ImageRepository + ":v9.9.9",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, worker.ImageForBuildVersion(tc.version))
		})
	}
}

// An unstamped build names no image at all, so the caller falls back rather
// than building a reference with an empty tag.
func TestImageForBuildVersionWithoutAVersion(t *testing.T) {
	t.Parallel()

	assert.Empty(t, worker.ImageForBuildVersion(""))
	assert.Empty(t, worker.ImageForBuildVersion("   "))
}
