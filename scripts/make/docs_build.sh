#!/bin/bash

set -euo pipefail

log_file="${LOG_FILE:-/tmp/peen-docs-build.log}"
readonly pages_image="ghcr.io/actions/jekyll-build-pages:v1.0.13@sha256:6791ebfd912185ed59bfb5fb102664fa872496b79f87ff8b9cfba292a7345041"
readonly site_directory=".testing/docs-site"

log() {
	local level="$1"
	shift

	printf '{"time":"%s","level":"%s","file":"%s","line":%d,"func":"%s","msg":"%s"}\n' \
		"$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')" \
		"${level}" \
		"${BASH_SOURCE[1]##*/}" \
		"${BASH_LINENO[0]}" \
		"${FUNCNAME[1]:-main}" \
		"$*" >&2
}

on_error() {
	local exit_code=$?
	log ERROR "documentation build failed exit=${exit_code}"
	exit "${exit_code}"
}

trap on_error ERR
exec > >(tee -a "${log_file}") 2>&1

mkdir -p "${site_directory}"
log INFO "building the documentation site image=${pages_image}"

# The image's entrypoint is what GitHub Pages runs, so a site that builds here
# builds there. It fetches the remote theme, which needs network access.
docker run --rm \
	--user "$(id -u):$(id -g)" \
	-e HOME=/tmp \
	-e GITHUB_WORKSPACE=/work \
	-e GITHUB_REPOSITORY=psyb0t/peen \
	-e INPUT_SOURCE=docs \
	-e "INPUT_DESTINATION=${site_directory}" \
	-e INPUT_VERBOSE=false \
	-v "${PWD}:/work" \
	"${pages_image}"

log INFO "documentation site built directory=${site_directory}"
