#!/bin/bash

set -euo pipefail

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

operation="${1:-}"
case "${operation}" in
audit | build | lock) ;;
*)
	log ERROR "operation must be audit, build, or lock"
	exit 1
	;;
esac

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source_root="${repository_root}/website"
temporary_root="$(mktemp -d)"
log_file="${LOG_FILE:-/tmp/peen-docs-${operation}.log}"

cleanup() {
	if [[ -d "${temporary_root}" ]]; then
		rm -rf "${temporary_root}"
	fi
}

on_error() {
	local exit_code=$?
	log ERROR "documentation ${operation} failed exit=${exit_code}"
	exit "${exit_code}"
}

trap cleanup EXIT
trap on_error ERR
exec > >(tee -a "${log_file}") 2>&1

# The site installs and builds in a scratch copy, so node_modules never lands
# in the checkout. Only the lockfile and the built site come back.
tar -C "${source_root}" \
	--exclude=./node_modules --exclude=./build \
	--exclude=./.content --exclude=./.assets --exclude=./.docusaurus \
	-cf - . | tar -C "${temporary_root}" -xf -
cd "${temporary_root}"

case "${operation}" in
lock)
	log INFO "refreshing the documentation site lockfile"
	corepack pnpm install --lockfile-only --ignore-scripts
	cp "${temporary_root}/pnpm-lock.yaml" "${source_root}/pnpm-lock.yaml"
	;;
audit)
	log INFO "auditing the documentation site dependencies"
	corepack pnpm audit
	;;
build)
	log INFO "preparing docs for the site"
	python3 -I "${repository_root}/scripts/make/docs_prepare.py" "${temporary_root}"
	corepack pnpm install --frozen-lockfile --ignore-scripts
	log INFO "building the documentation site"
	corepack pnpm run build
	rm -rf "${source_root}/build"
	cp -r "${temporary_root}/build" "${source_root}/build"
	log INFO "documentation site built directory=website/build"
	;;
esac
