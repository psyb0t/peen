package worker

import "strings"

const (
	// ImageRepository is the repository a controller's own worker image comes
	// from. A deployment may name any other image it likes.
	ImageRepository = "psyb0t/peen"

	// digestSeparator splits a reference into its repository and its digest.
	digestSeparator = "@"

	// tagSeparator introduces a tag.
	tagSeparator = ":"
)

// defaultRegistryPrefixes are the Docker Hub host forms an operator may write
// in front of the repository. They name the same repository, so all three are
// accepted and normalized away before the repository is compared.
//
//nolint:gochecknoglobals // A fixed lookup table, not mutable state.
var defaultRegistryPrefixes = []string{
	"docker.io/",
	"index.docker.io/",
	"registry-1.docker.io/",
}

// ImageForBuildVersion returns the worker image a controller of this build
// runs.
//
// The tag is the build's version verbatim. The worker and the controller are
// the same project, so a controller runs the image the same pipeline published
// beside it, and nothing here rewrites the version into some other tag.
//
// This is why a worker image is named by tag rather than by digest. A digest
// exists only after the push that creates it, so no release could name its own
// image ahead of time.
//
// A build that carries no release tag names an image its registry has no reason
// to hold. Set PEEN_WORKER_IMAGE to run a worker locally.
func ImageForBuildVersion(version string) string {
	tag := strings.TrimSpace(version)
	if tag == "" {
		return ""
	}

	return ImageRepository + tagSeparator + tag
}

// ImageRepositoryOf reduces a reference to its bare repository, dropping a
// Docker Hub host prefix, an optional tag, and any digest.
//
// It is how a repository digest the daemon reports is compared against the
// repository the operator asked for, so a digest belonging to some other
// repository is never recorded as this worker's image.
func ImageRepositoryOf(reference string) string {
	repository, _, _ := strings.Cut(reference, digestSeparator)
	repository = trimDefaultRegistry(repository)

	// Only a trailing tag is removed. A registry host with a port keeps its
	// colon, and that colon is always before a slash.
	//
	if index := strings.LastIndex(repository, tagSeparator); index >= 0 &&
		!strings.Contains(repository[index:], "/") {
		repository = repository[:index]
	}

	return repository
}

// trimDefaultRegistry drops a Docker Hub host alias, which names the same
// repository as the bare form.
func trimDefaultRegistry(repository string) string {
	for _, prefix := range defaultRegistryPrefixes {
		if trimmed, found := strings.CutPrefix(
			repository,
			prefix,
		); found {
			return trimmed
		}
	}

	return repository
}

// ImageDigestOf returns the sha256 digest a reference pins, or an empty string
// when it pins none.
func ImageDigestOf(reference string) string {
	_, digest, found := strings.Cut(reference, digestSeparator)
	if !found {
		return ""
	}

	return digest
}
