#!/bin/bash
#
# Installs the `peen` command for one release and sets Peen up for the user
# who runs it. It never starts Peen.
#
#   curl -fsSL https://raw.githubusercontent.com/psyb0t/peen/main/install.sh | bash
#   curl -fsSL .../install.sh | bash -s -- --control-space host --agent-space docker
#   curl -fsSL .../install.sh | sudo bash -s -- --system
#
# --user (default) puts the command in ~/.local/bin and must not run as root.
# --system puts it in /usr/local/bin, must run as root through sudo, and sets
# Peen up for the sudo user, because Peen always acts as a real user. Every
# other flag goes to `peen setup`.
#
# The script may arrive on stdin, so nothing it runs reads stdin. Output is for
# a person, so it prints plain "==> ..." lines rather than JSON, and keeps a
# copy in a private log file for when something fails.

set -Eeuo pipefail

readonly MARKER="# peen-managed-command"
readonly DEFAULT_SOURCE_URL="https://raw.githubusercontent.com/psyb0t/peen"
readonly DEFAULT_LATEST_URL="https://github.com/psyb0t/peen/releases/latest"
readonly ROLLING_TAG="latest"
readonly ROLLING_SOURCE_REF="main"
# v0.20.0 is the first release whose image has `peen config init`.
readonly MIN_MINOR_VERSION=20
readonly VERSION_PATTERN='^v([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?$'
readonly MODE_USER="user"
readonly MODE_SYSTEM="system"
readonly SYSTEM_INSTALL_PATH="/usr/local/bin/peen"
readonly USER_INSTALL_DIRECTORY=".local/bin"
readonly CONTROL_SPACE_HOST="host"
# shellcheck disable=SC2016 # The line goes into a shell rc file unexpanded.
readonly USER_PATH_SNIPPET='export PATH="$HOME/.local/bin:$PATH"'
# Settings a sudo'd setup must see; sudo resets the environment otherwise.
readonly FORWARDED_VARIABLES=(
	PEEN_HOME
	PEEN_INSTALL_REGISTRY_URL
	PEEN_INSTALL_REPOSITORY
	PEEN_INSTALL_SOURCE_URL
	PEEN_INSTALL_LATEST_URL
	PEEN_NO_BROWSER
	DOCKER_HOST
)

SOURCE_URL="${PEEN_INSTALL_SOURCE_URL:-$DEFAULT_SOURCE_URL}"
SOURCE_URL="${SOURCE_URL%/}"
LATEST_URL="${PEEN_INSTALL_LATEST_URL:-$DEFAULT_LATEST_URL}"
WORK_DIR=""
LOG_FILE=""
TEE_PIDS=()

say() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }

fail() {
	printf 'error: %s\n' "$*" >&2
	if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
		: >"$WORK_DIR/failed"
	fi
	exit 1
}

on_error() {
	local status="$1" line="$2"
	if [[ -n "$WORK_DIR" && -e "$WORK_DIR/failed" ]]; then
		return
	fi
	printf 'error: the installer stopped at line %s (exit %s); the log is %s\n' "$line" "$status" "$LOG_FILE" >&2
}

cleanup() {
	# WORK_DIR is this run's own mktemp directory with the downloaded command.
	if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
		rm -rf -- "$WORK_DIR"
	fi
	if ((${#TEE_PIDS[@]} == 0)); then
		return
	fi
	# Closing both streams lets the tees drain and exit, and waiting for them
	# keeps their last lines from landing after the shell prompt. A bash too
	# old to wait for a process substitution reports that, and the output is
	# complete either way.
	exec >&- 2>&-
	wait "${TEE_PIDS[@]}" 2>/dev/null || true
}

usage() {
	cat <<'EOF'
Usage: install.sh [--user|--system] [--rolling | --version vX.Y.Z] [setup flags]

  --user                         Install to ~/.local/bin/peen (default, not as root)
  --system                       Install to /usr/local/bin/peen (as root through sudo)
  --rolling                      Follow the moving :latest image and the main branch
  --version vX.Y.Z               Install this release instead of the newest one
  --control-space host|docker    Where Peen runs (default docker)
  --agent-space control|docker   Where each workspace's agent runs (default docker)
  --workspace-root DIR           The directory Peen may open workspaces in
  --listen ADDR                  Where Peen is reachable (default 127.0.0.1:8080)
  --container-name NAME          The controller container's name
EOF
}

check_supported_tag() {
	local tag="$1"
	[[ "$tag" =~ $VERSION_PATTERN ]] || fail "$tag is not a release version like v0.20.0"
	if ((10#${BASH_REMATCH[1]} == 0 && 10#${BASH_REMATCH[2]} < MIN_MINOR_VERSION)); then
		fail "$tag is older than v0.$MIN_MINOR_VERSION.0, which has no peen config init; install it by hand as docs/deployment.md describes"
	fi
}

latest_release_tag() {
	local effective tag
	effective="$(curl -fsSL -o /dev/null -w '%{url_effective}' "$LATEST_URL" </dev/null)" ||
		fail "could not reach $LATEST_URL to find the latest release"
	tag="${effective##*/tag/}"
	tag="${tag%%[?#]*}"
	tag="${tag%/}"
	[[ "$effective" == */tag/* && "$tag" =~ $VERSION_PATTERN ]] ||
		fail "could not read a release tag from $effective"
	printf '%s' "$tag"
}

is_managed_command() {
	[[ "$(sed -n 2p "$1")" == "$MARKER" ]]
}

# install_command puts the command in place by renaming a copy made in the
# same directory, so a shell running the old file keeps reading its inode.
install_command() {
	local downloaded="$1" path="$2" directory temporary
	directory="$(dirname -- "$path")"
	if [[ -e "$path" || -L "$path" ]] && ! is_managed_command "$path"; then
		fail "$path already exists and was not installed by Peen; move it away and run this again"
	fi
	mkdir -p -- "$directory"
	temporary="$directory/.peen.new.$$"
	install -m 0755 "$downloaded" "$temporary"
	mv -f -- "$temporary" "$path"
	say "installed $path"
}

warn_user_path() {
	local directory="$1"
	case ":$PATH:" in
	*":$directory:"*) return ;;
	esac
	warn "$directory is not on your PATH, so the peen command will not be found yet"
	printf '\nAdd it to your shell, then open a new shell:\n\n'
	printf "  bash:  echo '%s' >> ~/.bashrc\n" "$USER_PATH_SNIPPET"
	printf "  zsh:   echo '%s' >> ~/.zshrc\n\n" "$USER_PATH_SNIPPET"
}

# as_target_user runs a command as the user Peen is set up for: this user for
# --user, SUDO_USER for --system, with the settings setup needs forwarded.
as_target_user() {
	local mode="$1" user="$2" tag="$3" name
	local -a environment=()
	shift 3
	if [[ "$tag" != "$ROLLING_TAG" ]]; then
		environment+=("PEEN_INSTALL_VERSION=$tag")
	fi
	if [[ "$mode" == "$MODE_USER" ]]; then
		env "${environment[@]}" "$@" </dev/null
		return
	fi
	for name in "${FORWARDED_VARIABLES[@]}"; do
		if [[ -n "${!name:-}" ]]; then
			environment+=("$name=${!name}")
		fi
	done
	sudo -u "$user" -H env "${environment[@]}" "$@" </dev/null
}

main() {
	local mode="" rolling=0 version="" tag reference install_path target_user="" control_space="" recorded_control=""
	local -a setup_arguments=()

	while (($#)); do
		case "$1" in
		--user | --system)
			[[ -z "$mode" || "$mode" == "${1#--}" ]] || fail "--user and --system cannot be used together"
			mode="${1#--}"
			shift
			;;
		--rolling)
			rolling=1
			shift
			;;
		--version)
			[[ $# -ge 2 ]] || fail "--version needs a release like v0.20.0"
			version="$2"
			shift 2
			;;
		--control-space | --agent-space | --workspace-root | --listen | --container-name)
			[[ $# -ge 2 ]] || fail "$1 needs a value"
			if [[ "$1" == "--control-space" ]]; then
				control_space="$2"
			fi
			setup_arguments+=("$1" "$2")
			shift 2
			;;
		-h | --help)
			usage
			exit 0
			;;
		*) fail "unknown flag: $1 (see install.sh --help)" ;;
		esac
	done
	mode="${mode:-$MODE_USER}"

	if [[ "$mode" == "$MODE_SYSTEM" ]]; then
		((EUID == 0)) || fail "--system needs root; run it through sudo"
		target_user="${SUDO_USER:-}"
		[[ -n "$target_user" && "$target_user" != "root" ]] ||
			fail "--system sets Peen up for the user who ran sudo, and SUDO_USER is empty or root; run it as sudo from your own account"
		id "$target_user" >/dev/null || fail "the sudo user $target_user does not exist"
		install_path="$SYSTEM_INSTALL_PATH"
	else
		((EUID != 0)) || fail "do not install for root; run as your own account, or use --system through sudo"
		[[ -n "${HOME:-}" && "$HOME" == /* ]] || fail "HOME must be an absolute path"
		install_path="$HOME/$USER_INSTALL_DIRECTORY/peen"
	fi
	command -v curl >/dev/null 2>&1 || fail "the installer needs curl"

	if ((rolling)); then
		[[ -z "$version" && -z "${PEEN_INSTALL_VERSION:-}" ]] ||
			fail "--rolling follows :latest and cannot be combined with a version"
		tag="$ROLLING_TAG"
		reference="$ROLLING_SOURCE_REF"
		setup_arguments+=(--rolling)
	else
		tag="${version:-${PEEN_INSTALL_VERSION:-}}"
		if [[ -z "$tag" ]]; then
			tag="$(latest_release_tag)"
		fi
		check_supported_tag "$tag"
		reference="$tag"
	fi

	say "installing the peen command for $tag ($mode install)"
	curl -fsSL -o "$WORK_DIR/peen.sh" "$SOURCE_URL/$reference/scripts/peen.sh" </dev/null ||
		fail "could not download $SOURCE_URL/$reference/scripts/peen.sh"
	is_managed_command "$WORK_DIR/peen.sh" || fail "the downloaded file is not the peen command"
	bash -n "$WORK_DIR/peen.sh" || fail "the downloaded peen command is not valid bash"
	install_command "$WORK_DIR/peen.sh" "$install_path"

	if [[ "$mode" == "$MODE_SYSTEM" ]]; then
		# A re-install keeps the recorded control space when no flag names one.
		# A missing .env is the first install, and config get says so on stderr.
		if [[ -z "$control_space" ]]; then
			recorded_control="$(as_target_user "$mode" "$target_user" "$tag" "$install_path" config get PEEN_CONTROL_SPACE 2>/dev/null)" ||
				recorded_control=""
		fi
		if [[ "${control_space:-$recorded_control}" == "$CONTROL_SPACE_HOST" ]]; then
			local -a binary_arguments=()
			if ((rolling)); then
				binary_arguments=(--rolling)
			fi
			if [[ "$tag" != "$ROLLING_TAG" ]]; then
				PEEN_INSTALL_VERSION="$tag" "$install_path" __install-binary "${binary_arguments[@]}" </dev/null
			else
				"$install_path" __install-binary "${binary_arguments[@]}" </dev/null
			fi
		fi
	fi

	as_target_user "$mode" "$target_user" "$tag" "$install_path" setup "${setup_arguments[@]}" ||
		fail "peen setup failed; fix the problem above and run this installer again, or run peen setup"

	if [[ "$mode" == "$MODE_USER" ]]; then
		warn_user_path "$(dirname -- "$install_path")"
	fi
	printf '\nPeen %s is installed. Peen is not running yet.\n\n' "$tag"
	printf 'Next:\n'
	printf '  1. Add a model provider key:  peen config edit\n'
	printf '  2. Open a project:            cd ~/your/project && peen\n\n'
	printf 'peen help lists every command. peen doctor checks the installation.\n'
}

trap 'on_error "$?" "$LINENO"' ERR
trap cleanup EXIT
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/peen-install.XXXXXXXX")"
chmod 0700 "$WORK_DIR"
LOG_FILE="$(mktemp "${TMPDIR:-/tmp}/peen-install-log.XXXXXXXX")"
# Two tees keep errors on stderr for the person and both streams in the log.
exec > >(tee -a "$LOG_FILE")
TEE_PIDS+=("$!")
exec 2> >(tee -a "$LOG_FILE" >&2)
TEE_PIDS+=("$!")

main "$@"
