#!/bin/bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../.." && pwd)"
log_file="${LOG_FILE:-/tmp/peen-test-real.log}"

log() {
	local level="$1"
	shift

	jq -cn \
		--arg time "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
		--arg level "${level}" \
		--arg file "${BASH_SOURCE[1]##*/}" \
		--argjson line "${BASH_LINENO[0]}" \
		--arg func "${FUNCNAME[1]:-main}" \
		--arg msg "$*" \
		'{time: $time, level: $level, file: $file, line: $line, func: $func, msg: $msg}' \
		>&2
}

on_error() {
	local exit_code=$?
	log ERROR "command failed exit=${exit_code}"
	exit "${exit_code}"
}

trap on_error ERR
exec > >(tee -a "${log_file}") 2>&1

cleanup() {
	if [[ -n "${normalized_env_file:-}" && -e "${normalized_env_file}" ]]; then
		rm -f "${normalized_env_file}"
	fi
}

trap cleanup EXIT

if ! command -v docker >/dev/null; then
	log ERROR "docker is required"
	exit 1
fi

if ! command -v jq >/dev/null; then
	log ERROR "jq is required"
	exit 1
fi

cd "${repo_root}"
normalized_env_file=""
env_file_args=()

if [[ ! -f .env ]]; then
	log WARN "no .env found, real provider calls will skip"
else
	umask 077
	normalized_env_file="$(mktemp)"
	sed -E \
		-e "s/^([^=]+)='(.*)'$/\\1=\\2/" \
		-e 's/^([^=]+)="(.*)"$/\1=\2/' \
		.env >"${normalized_env_file}"
	env_file_args=(--env-file "${normalized_env_file}")
	log INFO "using deployment .env without printing values"
fi

if [[ -n "${DEBUG:-}" ]]; then
	log DEBUG "building the development image and starting tagged real-provider tests"
fi

if ! make dev-image; then
	log ERROR "build development image"
	exit 1
fi

if ! docker run --rm --init \
	--network host \
	--user "$(id -u):$(id -g)" \
	"${env_file_args[@]}" \
	-e HOME=/tmp \
	-e GOPATH=/tmp/go \
	-e GOCACHE=/tmp/go-cache \
	-e GOMODCACHE=/tmp/go-mod-cache \
	-v "${repo_root}:${repo_root}" \
	-w "${repo_root}" \
	peen-dev \
	go test -race -tags real -count=1 -timeout=600s ./tests/real/...; then
	log ERROR "run real provider tests"
	exit 1
fi

log INFO "real provider tests passed"
