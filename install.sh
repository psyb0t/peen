#!/bin/bash
#
# One-shot installer. Clones Peen into a temporary directory, builds the binary,
# installs it, and removes the clone.
#
#   curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash
#
# Environment:
#   PREFIX  where the binary lands (default: ~/bin)
#   REF     branch, tag, or commit to install (default: the default branch)
#
# Docker is required. The build runs in a pinned Go image, so no local Go
# toolchain is needed.

set -euo pipefail

readonly REPO_URL="${REPO_URL:-https://github.com/psyb0t/peen.git}"
readonly PREFIX="${PREFIX:-$HOME/bin}"
readonly REF="${REF:-}"

log() {
	local level="$1"
	shift

	printf '[%s] %s\n' "$level" "$*" >&2
}

workdir=""

cleanup() {
	# The clone is disposable and lives only in this run's own temporary
	# directory, so it goes whether the install succeeded or failed.
	if [[ -n "$workdir" && -d "$workdir" ]]; then
		log INFO "Removing $workdir"
		rm -rf -- "$workdir"
	fi
}

trap cleanup EXIT

for tool in git docker; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		log ERROR "$tool is required and was not found on PATH"
		exit 1
	fi
done

workdir="$(mktemp -d -t peen-install-XXXXXXXX)"
log INFO "Cloning $REPO_URL"

clone_args=(--depth 1)
if [[ -n "$REF" ]]; then
	clone_args+=(--branch "$REF")
fi

git clone "${clone_args[@]}" "$REPO_URL" "$workdir/peen"

cd "$workdir/peen"

log INFO "Building"
make build

log INFO "Installing to $PREFIX"
PREFIX="$PREFIX" make install

log INFO "Done"
