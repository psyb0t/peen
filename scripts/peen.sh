#!/bin/bash
# peen-managed-command
#
# The `peen` command. install.sh puts it at ~/.local/bin/peen or
# /usr/local/bin/peen; the line above marks the file as one the installer and
# `peen upgrade` may replace. It sets Peen up for one user, starts and stops the
# controller in the recorded control space, and opens workspaces in the browser.
#
# Output is for a person, not a log pipeline: progress on stdout as "==> ...",
# warnings and errors on stderr. It does not tee to a log file, because
# `config edit`, `logs -f`, and `run` need the real terminal on stdout.

set -Eeuo pipefail

# Empty arrays under `set -u` need bash 4.4.
if ((BASH_VERSINFO[0] < 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] < 4))); then
	printf 'error: peen needs bash 4.4 or newer\n' >&2
	exit 1
fi

readonly MARKER="# peen-managed-command"
readonly DEFAULT_REGISTRY_URL="https://registry-1.docker.io"
readonly DEFAULT_REPOSITORY="psyb0t/peen"
readonly DEFAULT_SOURCE_URL="https://raw.githubusercontent.com/psyb0t/peen"
readonly DEFAULT_LATEST_URL="https://github.com/psyb0t/peen/releases/latest"
readonly ROLLING_TAG="latest"
readonly ROLLING_SOURCE_REF="main"
# v0.20.0 is the first release whose image has `peen config init`.
readonly MIN_MINOR_VERSION=20
readonly VERSION_PATTERN='^v([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?$'
readonly KEY_PATTERN='^[A-Za-z_][A-Za-z0-9_]*$'
readonly DIGEST_PATTERN='^sha256:[0-9a-f]{64}$'
readonly CONTAINER_NAME_PATTERN='^[A-Za-z0-9][A-Za-z0-9_.-]*$'
readonly REPOSITORY_PATTERN='^[a-z0-9]+([._/-][a-z0-9]+)*$'
readonly SESSION_ID_PATTERN='^[0-9a-fA-F-]{36}$'
readonly CONTROL_SPACE_HOST="host"
readonly CONTROL_SPACE_DOCKER="docker"
readonly AGENT_SPACE_CONTROL="control"
readonly AGENT_SPACE_DOCKER="docker"
readonly AGENT_SPACE_DOCKER_DEFAULT="docker:default"
readonly CONTAINER_PORT=8080
readonly DEFAULT_LISTEN="127.0.0.1:8080"
readonly LOOPBACK_HOST="127.0.0.1"
readonly DEFAULT_DOCKER_SOCKET="/var/run/docker.sock"
readonly INSTALL_LABEL="peen.install"
readonly WORKER_LABEL="peen.managed=true"
readonly BINARY_MEMBER="app/app"
readonly CONTAINER_BINARY="/app/app"
readonly SERVER_COMMAND_SESSION="session"
readonly SERVER_COMMAND_CONTROL="control"
readonly START_TIMEOUT_SECONDS=90
readonly STOP_TIMEOUT_SECONDS=30
readonly DOCKER_INFO_TIMEOUT_SECONDS=20
readonly HEALTH_TIMEOUT_SECONDS=3
readonly API_TIMEOUT_SECONDS=60
readonly LOG_TAIL_ON_FAILURE=40
readonly DEFAULT_LOG_LINES=100
readonly STATUS_STOPPED=3
readonly PLACEHOLDER_VALUE="REPLACE_ME"
readonly MASKED_VALUE="********"
readonly SECRET_NAME_PATTERN='(TOKEN|KEY|SECRET|PASSWORD)'
readonly HEADER_SESSION_ID="x-session-id"
readonly HEADER_AUTHENTICATE="www-authenticate"
readonly ACCEPT_MANIFESTS="application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

REGISTRY_URL="${PEEN_INSTALL_REGISTRY_URL:-$DEFAULT_REGISTRY_URL}"
REGISTRY_URL="${REGISTRY_URL%/}"
REPOSITORY="${PEEN_INSTALL_REPOSITORY:-$DEFAULT_REPOSITORY}"
SOURCE_URL="${PEEN_INSTALL_SOURCE_URL:-$DEFAULT_SOURCE_URL}"
SOURCE_URL="${SOURCE_URL%/}"
LATEST_URL="${PEEN_INSTALL_LATEST_URL:-$DEFAULT_LATEST_URL}"

PEEN_HOME_DIR=""
ENV_FILE=""
WRAPPER_PATH=""
LIB_DIR=""
BINARY=""
BINARY_IMAGE_FILE=""
WORK_DIR=""
REGISTRY_HEADER_FILE=""
DOCTOR_FAILURES=0
ENV_ASSIGNMENTS=()
HOST_ENVIRONMENT=()
DOCKER_ARGUMENTS=()

say() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }

# fail leaves a marker in WORK_DIR before it exits. A fail inside a command
# substitution exits only that subshell, and the marker tells the caller's ERR
# trap that the reason is already printed.
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
	printf 'error: peen stopped at line %s (exit %s)\n' "$line" "$status" >&2
}

cleanup() {
	# WORK_DIR is this run's own mktemp directory. It holds downloads and
	# header files, some with a registry or API token, so it never outlives
	# the run.
	if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
		rm -rf -- "$WORK_DIR"
	fi
}

trap 'on_error "$?" "$LINENO"' ERR
trap cleanup EXIT

usage() {
	cat <<'EOF'
Usage: peen [command] [arguments]

  peen                      Open the current directory in the browser
  peen <dir>                Open DIR (any argument with a slash, or . or ..)
  peen open [dir]           Open DIR, default the current directory

  peen setup [flags]        Create or update the configuration and pin the image
      --control-space host|docker   Where Peen runs (default docker)
      --agent-space control|docker  Where each workspace's agent runs (default docker)
      --workspace-root DIR          The directory Peen may open workspaces in
      --listen ADDR                 Where Peen is reachable (default 127.0.0.1:8080)
      --container-name NAME         The controller container's name
      --rolling                     Use the moving :latest image
  peen start | stop | restart       Run the controller in the background
  peen status                       Show where Peen runs and whether it answers
  peen logs [-f] [N]                Show the last N controller log lines
  peen run [dir]                    Run a foreground controller rooted at DIR
  peen config edit|show|path        Edit, print with secrets masked, or locate .env
  peen config get KEY               Print one setting
  peen config set KEY VALUE         Change one setting
  peen doctor                       Check the installation
  peen session open|list|attach|stop ...
                                    Run the server's session commands
  peen control status               Report whether the controller answers
  peen upgrade [--rolling]          Move to the latest release and restart
  peen uninstall [--keep-data|--delete-data]
  peen version | help

Configuration lives in $PEEN_HOME, default ~/.config/peen.
EOF
}

resolve_paths() {
	local home_dir="${PEEN_HOME:-${XDG_CONFIG_HOME:-${HOME:?HOME is not set}/.config}/peen}"
	[[ "$home_dir" == /* ]] || fail "PEEN_HOME must be an absolute path"
	PEEN_HOME_DIR="${home_dir%/}"
	ENV_FILE="$PEEN_HOME_DIR/.env"

	WRAPPER_PATH="$(readlink -f -- "${BASH_SOURCE[0]}")"
	[[ -n "$WRAPPER_PATH" && -f "$WRAPPER_PATH" ]] ||
		fail "could not resolve the path of this peen command"
	LIB_DIR="$(dirname -- "$(dirname -- "$WRAPPER_PATH")")/lib/peen"
	BINARY="$LIB_DIR/peen"
	BINARY_IMAGE_FILE="$LIB_DIR/image"
}

work_dir() {
	if [[ -n "$WORK_DIR" ]]; then
		return
	fi
	WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/peen.XXXXXXXX")"
	chmod 0700 "$WORK_DIR"
}

require_env_file() {
	[[ -f "$ENV_FILE" ]] || fail "$ENV_FILE does not exist; run peen setup first"
}

# env_get prints the value of KEY's last active line, the one Docker's
# --env-file uses, and fails when no active line sets it. Leading space is
# trimmed inline, because a subshell per line makes every lookup slow.
env_get() {
	local key="$1" line stripped value="" found=1

	[[ -f "$ENV_FILE" ]] || return 1
	while IFS= read -r line || [[ -n "$line" ]]; do
		stripped="${line#"${line%%[![:space:]]*}"}"
		if [[ "$stripped" != "$key="* ]]; then
			continue
		fi
		value="${stripped#*=}"
		found=0
	done <"$ENV_FILE"

	if ((found != 0)); then
		return 1
	fi
	printf '%s' "$value"
}

# env_value is env_get for callers where a missing key means empty.
env_value() {
	env_get "$1" || true
}

validate_key() {
	[[ "$1" =~ $KEY_PATTERN ]] || fail "invalid setting name: $1"
}

validate_value() {
	case "$2" in
	*$'\n'* | *$'\r'*) fail "the value for $1 must be a single line" ;;
	esac
}

# env_set rewrites .env through a temporary file in the same directory, so a
# reader never sees half a file and the value is never parsed by sed. The first
# active line for KEY is replaced and later duplicates dropped. Without one, the
# first commented "# KEY=" line from the template is replaced, so the setting
# stays next to its documentation; otherwise the line is appended.
env_set() {
	local key="$1" value="$2" line stripped temporary replace_comment=0 written=0

	validate_key "$key"
	validate_value "$key" "$value"
	require_env_file

	if ! env_get "$key" >/dev/null; then
		replace_comment=1
	fi

	temporary="$(mktemp "$PEEN_HOME_DIR/.env.XXXXXXXX")"
	chmod 0600 "$temporary"
	{
		while IFS= read -r line || [[ -n "$line" ]]; do
			stripped="${line#"${line%%[![:space:]]*}"}"
			if ((replace_comment == 0)) && [[ "$stripped" == "$key="* ]]; then
				if ((written == 0)); then
					printf '%s=%s\n' "$key" "$value"
					written=1
				fi
				continue
			fi
			if ((replace_comment == 1 && written == 0)) &&
				[[ "$stripped" =~ ^#[[:space:]]*"$key"= ]]; then
				printf '%s=%s\n' "$key" "$value"
				written=1
				continue
			fi
			printf '%s\n' "$line"
		done <"$ENV_FILE"
		if ((written == 0)); then
			printf '%s=%s\n' "$key" "$value"
		fi
	} >"$temporary"
	mv -f -- "$temporary" "$ENV_FILE"
}

# read_env_assignments parses .env with Docker --env-file rules: leading space
# trimmed, blank and # lines skipped, split on the first "=", the value kept
# verbatim, and a bare NAME taken from this environment when set. The file is
# never sourced, because PEEN_UPSTREAMS holds raw JSON that is not shell.
read_env_assignments() {
	local line stripped key value number=0

	ENV_ASSIGNMENTS=()
	require_env_file
	while IFS= read -r line || [[ -n "$line" ]]; do
		number=$((number + 1))
		stripped="${line#"${line%%[![:space:]]*}"}"
		if [[ -z "$stripped" || "$stripped" == \#* ]]; then
			continue
		fi
		key="${stripped%%=*}"
		[[ "$key" =~ $KEY_PATTERN ]] || fail "$ENV_FILE line $number: invalid variable name"
		if [[ "$stripped" == *=* ]]; then
			value="${stripped#*=}"
		elif [[ -n "${!key+set}" ]]; then
			value="${!key}"
		else
			continue
		fi
		ENV_ASSIGNMENTS+=("$key=$value")
	done <"$ENV_FILE"
}

host_username() {
	local name
	# id -un fails for a numeric UID without a passwd entry, as in some
	# containers, and the login environment is then the only other source.
	if name="$(id -un 2>/dev/null)" && [[ -n "$name" ]]; then
		printf '%s' "$name"
		return
	fi
	[[ -n "${USER:-}" ]] || fail "cannot determine your user name; set USER"
	printf '%s' "$USER"
}

default_container_name() {
	local name
	name="peen-$(host_username)"
	printf '%s' "${name//[^A-Za-z0-9_.-]/-}"
}

refuse_root() {
	[[ "$(id -u)" != "0" ]] ||
		fail "do not run Peen as root; run this command as the user Peen should act as"
}

control_space() {
	local space
	space="$(env_value PEEN_CONTROL_SPACE)"
	case "$space" in
	"$CONTROL_SPACE_HOST" | "$CONTROL_SPACE_DOCKER") printf '%s' "$space" ;;
	"") fail "PEEN_CONTROL_SPACE is not set in $ENV_FILE; run peen setup" ;;
	*) fail "PEEN_CONTROL_SPACE must be host or docker, not $space" ;;
	esac
}

agent_space_is_docker() {
	[[ "$(env_value PEEN_DEFAULT_AGENT_SPACE)" == docker:* ]]
}

container_name() {
	local name
	name="$(env_value PEEN_CONTAINER_NAME)"
	if [[ -z "$name" ]]; then
		name="$(default_container_name)"
	fi
	[[ "$name" =~ $CONTAINER_NAME_PATTERN ]] || fail "PEEN_CONTAINER_NAME is not a valid container name: $name"
	printf '%s' "$name"
}

pinned_image() {
	local image
	image="$(env_value PEEN_IMAGE)"
	[[ -n "$image" ]] || fail "PEEN_IMAGE is not set in $ENV_FILE; run peen setup"
	printf '%s' "$image"
}

validate_listen() {
	local address="$1" port
	[[ "$address" =~ ^(\[[0-9A-Fa-f:.]+\]|[0-9]{1,3}(\.[0-9]{1,3}){3}|localhost)?:([0-9]{1,5})$ ]] ||
		fail "--listen must look like 127.0.0.1:8080, :8080, or [::1]:8080, not $address"
	port="${BASH_REMATCH[3]}"
	((10#$port >= 1 && 10#$port <= 65535)) || fail "the listen port must be between 1 and 65535"
}

listen_address() {
	local address
	address="$(env_value PEEN_HTTP_LISTEN_ADDRESS)"
	printf '%s' "${address:-$DEFAULT_LISTEN}"
}

# reach_host is the address a client on this machine uses: an all-interfaces
# listener is reached on loopback.
reach_host() {
	local host="${1%:*}"
	case "$host" in
	"" | 0.0.0.0 | "[::]" | "*") printf '%s' "$LOOPBACK_HOST" ;;
	*) printf '%s' "$host" ;;
	esac
}

listen_port() {
	printf '%s' "${1##*:}"
}

base_url() {
	local address
	address="$(listen_address)"
	printf 'http://%s:%s' "$(reach_host "$address")" "$(listen_port "$address")"
}

publish_spec() {
	local address host port
	address="$(listen_address)"
	host="${address%:*}"
	port="$(listen_port "$address")"
	case "$host" in
	"") printf '%s:%s' "$port" "$CONTAINER_PORT" ;;
	localhost) printf '%s:%s:%s' "$LOOPBACK_HOST" "$port" "$CONTAINER_PORT" ;;
	*) printf '%s:%s:%s' "$host" "$port" "$CONTAINER_PORT" ;;
	esac
}

# tcp_open succeeds when something accepts a connection on HOST:PORT. The
# subshell owns the descriptor, and a refused connect prints a bash error that
# carries no information beyond the exit status.
tcp_open() {
	(exec 3<>"/dev/tcp/$1/$2") 2>/dev/null
}

port_busy() {
	local host
	host="$(reach_host "$1")"
	host="${host#[}"
	host="${host%]}"
	tcp_open "$host" "$(listen_port "$1")"
}

# docker_works runs `docker info` with its output dropped: only whether the
# daemon answers matters here, and each caller prints its own reason.
docker_works() {
	command -v docker >/dev/null 2>&1 &&
		timeout "$DOCKER_INFO_TIMEOUT_SECONDS" docker info >/dev/null 2>&1
}

require_docker() {
	command -v docker >/dev/null 2>&1 || fail "$1 needs Docker, and the docker command is not installed"
	docker_works ||
		fail "$1 needs Docker, and 'docker info' fails; start the daemon or add your user to the docker group, then log in again"
}

# docker_socket prints the local socket the docker CLI talks to. Peen hands a
# Docker agent space that path at its literal host location, so a remote
# DOCKER_HOST cannot work.
docker_socket() {
	local host="${DOCKER_HOST:-}"
	case "$host" in
	"") printf '%s' "$DEFAULT_DOCKER_SOCKET" ;;
	unix://*) printf '%s' "${host#unix://}" ;;
	*) return 1 ;;
	esac
}

require_local_docker_socket() {
	local socket
	socket="$(docker_socket)" ||
		fail "DOCKER_HOST=${DOCKER_HOST:-} is not a local unix socket; Peen needs the daemon on this machine"
	[[ -S "$socket" ]] || fail "the Docker socket $socket does not exist; set DOCKER_HOST=unix:///path/to/docker.sock"
}

socket_usable() {
	[[ -S "$1" && -r "$1" && -w "$1" ]]
}

# docker_reachable is the wrapper's view of Peen's socket rule: any daemon this
# user can use makes a control agent host root. It is conservative and also
# counts a tcp:// DOCKER_HOST that accepts a connection.
docker_reachable() {
	local extra_socket="" tcp_target

	if docker_works || socket_usable "$DEFAULT_DOCKER_SOCKET"; then
		return 0
	fi
	case "${DOCKER_HOST:-}" in
	unix://*)
		if socket_usable "${DOCKER_HOST#unix://}"; then
			return 0
		fi
		;;
	tcp://*)
		tcp_target="${DOCKER_HOST#tcp://}"
		tcp_target="${tcp_target%%/*}"
		if tcp_open "${tcp_target%:*}" "${tcp_target##*:}"; then
			return 0
		fi
		;;
	esac
	if [[ -f "$ENV_FILE" ]]; then
		extra_socket="$(env_value PEEN_DOCKER_SOCKET)"
	fi
	[[ -n "$extra_socket" ]] && socket_usable "$extra_socket"
}

# container_state prints absent, foreign, running, or stopped for a container
# name. A container is ours only when its peen.install label names this
# PEEN_HOME, so another installation's or a stranger's container is never
# touched.
container_state() {
	local name="$1" details label running
	# A missing container makes inspect fail with a message that only says so.
	if ! details="$(docker container inspect -f "{{index .Config.Labels \"$INSTALL_LABEL\"}}|{{.State.Running}}" "$name" 2>/dev/null)"; then
		printf 'absent'
		return
	fi
	label="${details%|*}"
	running="${details##*|}"
	if [[ "$label" != "$PEEN_HOME_DIR" ]]; then
		printf 'foreign'
		return
	fi
	if [[ "$running" == "true" ]]; then
		printf 'running'
		return
	fi
	printf 'stopped'
}

refuse_foreign_container() {
	fail "a container named $1 exists and was not created by this installation ($INSTALL_LABEL is not $PEEN_HOME_DIR), so I will not touch it. Remove or rename it, or pick another name with peen config set PEEN_CONTAINER_NAME <name>"
}

state_dir() {
	local directory
	directory="$(env_value PEEN_STATE_DIR)"
	printf '%s' "${directory:-$PEEN_HOME_DIR/state}"
}

config_dir() {
	local directory
	directory="$(env_value PEEN_CONFIG_DIR)"
	printf '%s' "${directory:-$PEEN_HOME_DIR/config}"
}

pid_file() { printf '%s/controller.pid' "$(state_dir)"; }
log_file() { printf '%s/controller.log' "$(state_dir)"; }

workspace_root() {
	local root
	root="$(env_value PEEN_WORKSPACE_ROOT)"
	[[ -n "$root" ]] || fail "PEEN_WORKSPACE_ROOT is not set in $ENV_FILE; run peen setup --workspace-root DIR"
	printf '%s' "$root"
}

# host_pid prints the managed controller's PID when that process is alive and
# still runs this installation's binary, so a recycled PID is never signalled.
host_pid() {
	local file pid command_line
	file="$(pid_file)"
	[[ -f "$file" ]] || return 1
	pid="$(<"$file")"
	[[ "$pid" =~ ^[0-9]+$ ]] || return 1
	# kill -0 and reading /proc report a process that just exited as an error
	# message, and that case is the answer "not running".
	kill -0 "$pid" 2>/dev/null || return 1
	command_line="$(tr '\0' '\n' <"/proc/$pid/cmdline" 2>/dev/null)" || return 1
	[[ "$(head -n 1 <<<"$command_line")" == "$BINARY" ]] || return 1
	grep -qx 'run' <<<"$command_line" || return 1
	printf '%s' "$pid"
}

# host_environment is the host controller's whole environment: a few login
# variables and .env, nothing else from this shell, so unrelated secrets in the
# shell never reach a control agent.
host_environment() {
	local name username
	username="$(host_username)"
	HOST_ENVIRONMENT=("HOME=$HOME" "PATH=$PATH" "USER=$username" "LOGNAME=${LOGNAME:-$username}")
	for name in LANG LC_ALL TZ DOCKER_HOST; do
		if [[ -n "${!name:-}" ]]; then
			HOST_ENVIRONMENT+=("$name=${!name}")
		fi
	done
	read_env_assignments
	HOST_ENVIRONMENT+=("${ENV_ASSIGNMENTS[@]}")
}

controller_running() {
	if [[ "$(control_space)" == "$CONTROL_SPACE_DOCKER" ]]; then
		[[ "$(container_state "$(container_name)")" == "running" ]]
		return
	fi
	host_pid >/dev/null
}

# healthy drops curl's connection-refused message: a stopped controller is an
# expected answer and every caller reports it in its own words.
healthy() {
	curl -fsS -m "$HEALTH_TIMEOUT_SECONDS" -o /dev/null "$(base_url)/healthz" 2>/dev/null
}

print_log_tail() {
	local lines="$1"
	printf -- '---- last %s controller log lines ----\n' "$lines" >&2
	if [[ "$(control_space)" == "$CONTROL_SPACE_DOCKER" ]]; then
		if ! docker logs --tail "$lines" "$(container_name)" >&2 2>&1; then
			warn "could not read the container logs"
		fi
	elif [[ -f "$(log_file)" ]]; then
		tail -n "$lines" "$(log_file)" >&2
	fi
	printf -- '----\n' >&2
}

check_supported_tag() {
	local tag="$1"
	[[ "$tag" =~ $VERSION_PATTERN ]] || fail "$tag is not a release version like v0.20.0"
	if ((10#${BASH_REMATCH[1]} == 0 && 10#${BASH_REMATCH[2]} < MIN_MINOR_VERSION)); then
		fail "$tag is older than v0.$MIN_MINOR_VERSION.0, the first release this command can install"
	fi
}

latest_release_tag() {
	local effective tag
	effective="$(curl -fsSL -o /dev/null -w '%{url_effective}' "$LATEST_URL")" ||
		fail "could not reach $LATEST_URL to find the latest release"
	tag="${effective##*/tag/}"
	tag="${tag%%[?#]*}"
	tag="${tag%/}"
	[[ "$effective" == */tag/* && "$tag" =~ $VERSION_PATTERN ]] ||
		fail "could not read a release tag from $effective"
	printf '%s' "$tag"
}

# resolve_tag picks the release: :latest under --rolling, PEEN_INSTALL_VERSION
# when set (install.sh sets it so the command and the image match), otherwise
# the newest GitHub release through its redirect, which needs no API call.
resolve_tag() {
	local rolling="$1" tag
	if ((rolling)); then
		printf '%s' "$ROLLING_TAG"
		return
	fi
	tag="${PEEN_INSTALL_VERSION:-}"
	if [[ -z "$tag" ]]; then
		tag="$(latest_release_tag)"
	fi
	check_supported_tag "$tag"
	printf '%s' "$tag"
}

registry_host() {
	local host="${REGISTRY_URL#*://}"
	printf '%s' "${host%%/*}"
}

image_reference() {
	local tag="$1"
	[[ "$REPOSITORY" =~ $REPOSITORY_PATTERN ]] || fail "invalid repository: $REPOSITORY"
	if [[ "$REGISTRY_URL" == "$DEFAULT_REGISTRY_URL" ]]; then
		printf '%s:%s' "$REPOSITORY" "$tag"
		return
	fi
	printf '%s/%s:%s' "$(registry_host)" "$REPOSITORY" "$tag"
}

host_architecture() {
	[[ "$(uname -s)" == "Linux" ]] || fail "the host control space runs only on Linux; use --control-space docker"
	case "$(uname -m)" in
	x86_64 | amd64) printf 'amd64' ;;
	aarch64 | arm64) printf 'arm64' ;;
	*) fail "the host control space supports amd64 and arm64, not $(uname -m); use --control-space docker" ;;
	esac
}

require_host_tools() {
	local tool
	host_architecture >/dev/null
	for tool in curl jq tar sha256sum od; do
		command -v "$tool" >/dev/null 2>&1 || fail "the host control space needs $tool; install it and run this again"
	done
}

# registry_fetch GETs one registry URL into a file and prints the HTTP status.
# A 401 with a Bearer challenge fetches an anonymous pull token once and
# retries. The token travels in a header file, never on the command line, and
# curl does not forward it when a blob redirects to another host.
registry_fetch() {
	local url="$1" output="$2" accept="${3:-}" headers status challenge realm service scope token_json token
	local -a arguments

	work_dir
	headers="$WORK_DIR/registry-headers"
	arguments=(-sS -L -o "$output" -D "$headers" -w '%{http_code}')
	if [[ -n "$accept" ]]; then
		arguments+=(-H "Accept: $accept")
	fi
	if [[ -n "$REGISTRY_HEADER_FILE" ]]; then
		arguments+=(-H "@$REGISTRY_HEADER_FILE")
	fi
	status="$(curl "${arguments[@]}" "$url")" || fail "could not reach $url"
	if [[ "$status" != "401" || -n "$REGISTRY_HEADER_FILE" ]]; then
		printf '%s' "$status"
		return
	fi

	challenge="$(grep -i "^$HEADER_AUTHENTICATE:" "$headers" | tail -n 1 | tr -d '\r')" ||
		fail "the registry refused $url without a Bearer challenge"
	[[ "$challenge" =~ [Bb]earer ]] || fail "the registry refused $url without a Bearer challenge"
	realm="$(sed -n 's/.*realm="\([^"]*\)".*/\1/p' <<<"$challenge")"
	service="$(sed -n 's/.*service="\([^"]*\)".*/\1/p' <<<"$challenge")"
	scope="$(sed -n 's/.*scope="\([^"]*\)".*/\1/p' <<<"$challenge")"
	[[ -n "$realm" ]] || fail "the registry's Bearer challenge names no realm"
	scope="${scope:-repository:$REPOSITORY:pull}"

	token_json="$WORK_DIR/registry-token.json"
	arguments=(-fsS -G -o "$token_json" --data-urlencode "scope=$scope")
	if [[ -n "$service" ]]; then
		arguments+=(--data-urlencode "service=$service")
	fi
	curl "${arguments[@]}" "$realm" || fail "could not get a pull token from $realm"
	token="$(jq -r '.token // .access_token // empty' "$token_json")"
	[[ -n "$token" ]] || fail "the registry token response holds no token"
	REGISTRY_HEADER_FILE="$WORK_DIR/registry-auth"
	(
		umask 077
		printf 'Authorization: Bearer %s\n' "$token" >"$REGISTRY_HEADER_FILE"
	)
	registry_fetch "$url" "$output" "$accept"
}

file_sha256() {
	local sum
	sum="$(sha256sum "$1")"
	printf 'sha256:%s' "${sum%% *}"
}

fetch_manifest() {
	local reference="$1" output="$2" status
	status="$(registry_fetch "$REGISTRY_URL/v2/$REPOSITORY/manifests/$reference" "$output" "$ACCEPT_MANIFESTS")"
	[[ "$status" == "200" ]] || fail "the registry answered HTTP $status for $REPOSITORY:$reference"
	if [[ "$reference" == sha256:* && "$(file_sha256 "$output")" != "$reference" ]]; then
		fail "the manifest for $reference does not match its digest"
	fi
}

fetch_blob() {
	local digest="$1" output="$2" status
	[[ "$digest" =~ $DIGEST_PATTERN ]] || fail "unexpected layer digest: $digest"
	status="$(registry_fetch "$REGISTRY_URL/v2/$REPOSITORY/blobs/$digest" "$output")"
	[[ "$status" == "200" ]] || fail "the registry answered HTTP $status for blob $digest"
	[[ "$(file_sha256 "$output")" == "$digest" ]] || fail "layer $digest failed its sha256 check"
}

# layer_tar_flag prints the tar flag for a layer's compression, read from its
# magic bytes rather than the media type, which registries do not all set.
layer_tar_flag() {
	local magic
	magic="$(head -c 4 "$1" | od -An -tx1 | tr -d ' \n')"
	case "$magic" in
	1f8b*) printf -- '-z' ;;
	28b52ffd)
		# Only whether this tar can read zstd matters; its own error says less.
		tar --zstd -tf "$1" >/dev/null 2>&1 ||
			fail "the image layer is zstd-compressed and this tar cannot read it; install zstd and a tar with --zstd"
		printf -- '--zstd'
		;;
	esac
}

# download_binary pulls the controller binary out of a published image over the
# registry HTTP API, so the host control space needs no Docker daemon. It walks
# the platform's layers from the top, verifies each blob's sha256, and extracts
# only app/app.
download_binary() {
	local image="$1" tag architecture manifest platform_manifest digest layer listing member flag destination
	local -a layers tar_flags

	require_host_tools
	architecture="$(host_architecture)"
	tag="${image##*:}"
	work_dir
	manifest="$WORK_DIR/manifest.json"
	say "downloading the Peen binary for linux/$architecture from $REPOSITORY:$tag"
	fetch_manifest "$tag" "$manifest"

	if jq -e '.manifests' "$manifest" >/dev/null; then
		digest="$(jq -r --arg arch "$architecture" '
			[.manifests[]
				| select(.platform.os == "linux" and .platform.architecture == $arch)
				| select($arch != "arm64" or ((.platform.variant // "v8") == "v8"))
			][0].digest // empty' "$manifest")"
		[[ -n "$digest" ]] || fail "$REPOSITORY:$tag has no linux/$architecture image"
		platform_manifest="$WORK_DIR/platform-manifest.json"
		fetch_manifest "$digest" "$platform_manifest"
		manifest="$platform_manifest"
	fi
	jq -e '.layers | length > 0' "$manifest" >/dev/null || fail "$REPOSITORY:$tag has no image layers"
	mapfile -t layers < <(jq -r '.layers | reverse | .[].digest' "$manifest")

	layer="$WORK_DIR/layer"
	destination="$WORK_DIR/extract"
	member=""
	for digest in "${layers[@]}"; do
		fetch_blob "$digest" "$layer"
		flag="$(layer_tar_flag "$layer")"
		tar_flags=()
		if [[ -n "$flag" ]]; then
			tar_flags=("$flag")
		fi
		listing="$(tar "${tar_flags[@]}" -tf "$layer")" || fail "could not read layer $digest"
		member="$(grep -xE "(\./)?$BINARY_MEMBER" <<<"$listing" | head -n 1)" || member=""
		if [[ -n "$member" ]]; then
			mkdir -p "$destination"
			tar "${tar_flags[@]}" -xf "$layer" -C "$destination" "$member" ||
				fail "could not extract $BINARY_MEMBER from layer $digest"
			break
		fi
		if grep -qxE '(\./)?(\.wh\.app|app/\.wh\.app|app/\.wh\.\.wh\.\.opq)' <<<"$listing"; then
			fail "$REPOSITORY:$tag removes $BINARY_MEMBER in a later layer"
		fi
	done
	[[ -n "$member" && -f "$destination/$BINARY_MEMBER" ]] || fail "$REPOSITORY:$tag holds no $BINARY_MEMBER"

	chmod 0755 "$destination/$BINARY_MEMBER"
	# The help text itself is not needed, only proof the binary runs here.
	"$destination/$BINARY_MEMBER" --help >/dev/null 2>&1 ||
		fail "the downloaded binary does not run on this machine"
	install_file "$destination/$BINARY_MEMBER" "$BINARY" 0755
	printf '%s\n' "$image" >"$WORK_DIR/image"
	install_file "$WORK_DIR/image" "$BINARY_IMAGE_FILE" 0644
	say "installed $BINARY"
}

binary_matches() {
	[[ -x "$BINARY" && -f "$BINARY_IMAGE_FILE" && "$(<"$BINARY_IMAGE_FILE")" == "$1" ]]
}

# install_file replaces DEST by renaming a copy made in the same directory, so
# a running controller or this very script keeps reading its old inode. A
# directory this user cannot write, as in a --system install, goes through sudo.
install_file() {
	local source="$1" destination="$2" mode="$3" directory temporary
	local -a elevate=()

	directory="$(dirname -- "$destination")"
	# mkdir's permission error is the case the sudo branch below handles.
	if ! mkdir -p -- "$directory" 2>/dev/null || [[ ! -w "$directory" ]]; then
		command -v sudo >/dev/null 2>&1 || fail "writing $directory needs root and sudo is not installed"
		elevate=(sudo)
		sudo mkdir -p -- "$directory"
	fi
	temporary="$directory/.$(basename -- "$destination").new.$$"
	"${elevate[@]}" install -m "$mode" "$source" "$temporary"
	"${elevate[@]}" mv -f -- "$temporary" "$destination"
}

canonical_directory() {
	# A missing directory is reported by the caller with its own context.
	(cd -- "$1" 2>/dev/null && pwd -P)
}

# validate_combination refuses a control space and agent space pair this
# machine cannot run safely. It runs before setup changes anything.
validate_combination() {
	local control="$1" agent="$2"

	if [[ "$control" == "$CONTROL_SPACE_HOST" ]]; then
		require_host_tools
	else
		require_docker "the docker control space"
		docker_socket >/dev/null ||
			fail "DOCKER_HOST=${DOCKER_HOST:-} is not a local unix socket; the docker control space mounts paths of this machine"
	fi

	if [[ "$agent" == "$AGENT_SPACE_DOCKER" ]]; then
		require_docker "the docker agent space"
		require_local_docker_socket
	fi

	if [[ "$control" == "$CONTROL_SPACE_HOST" && "$agent" == "$AGENT_SPACE_CONTROL" ]] && docker_reachable; then
		fail "--control-space host --agent-space control is refused: your user can reach a Docker daemon, and a reachable daemon makes a control agent host root, because it can start a container that mounts /. Use --agent-space docker to give each workspace its own container, use --control-space docker --agent-space control to keep the agent inside the controller container, or run as a user without Docker access"
	fi
}

setup() {
	local rolling=0 control="" agent="" root="" listen="" name="" root_given=0 first=1
	local recorded_control="" recorded_agent image agent_space_value effective_name control_given agent_given

	while (($#)); do
		case "$1" in
		--rolling)
			rolling=1
			shift
			continue
			;;
		--control-space | --agent-space | --workspace-root | --listen | --container-name)
			[[ $# -ge 2 ]] || fail "$1 needs a value"
			;;
		*) fail "unknown setup flag: $1 (see peen help)" ;;
		esac
		case "$1" in
		--control-space) control="$2" ;;
		--agent-space) agent="$2" ;;
		--workspace-root)
			root="$2"
			root_given=1
			;;
		--listen) listen="$2" ;;
		--container-name) name="$2" ;;
		esac
		shift 2
	done

	refuse_root
	case "$control" in "" | "$CONTROL_SPACE_HOST" | "$CONTROL_SPACE_DOCKER") ;; *) fail "--control-space must be host or docker" ;; esac
	case "$agent" in "" | "$AGENT_SPACE_CONTROL" | "$AGENT_SPACE_DOCKER") ;; *) fail "--agent-space must be control or docker" ;; esac
	if [[ -n "$listen" ]]; then
		validate_listen "$listen"
	fi
	if [[ -n "$name" && ! "$name" =~ $CONTAINER_NAME_PATTERN ]]; then
		fail "--container-name may hold letters, digits, '_', '.', and '-', starting with a letter or digit"
	fi
	if ((rolling)) && [[ -n "${PEEN_INSTALL_VERSION:-}" ]]; then
		fail "--rolling and PEEN_INSTALL_VERSION cannot be used together"
	fi
	control_given=0
	agent_given=0
	if [[ -n "$control" ]]; then
		control_given=1
	fi
	if [[ -n "$agent" ]]; then
		agent_given=1
	fi

	if [[ -f "$ENV_FILE" ]]; then
		first=0
		recorded_control="$(env_value PEEN_CONTROL_SPACE)"
		recorded_agent="$AGENT_SPACE_CONTROL"
		if agent_space_is_docker; then
			recorded_agent="$AGENT_SPACE_DOCKER"
		fi
		control="${control:-$recorded_control}"
		agent="${agent:-$recorded_agent}"
		if ((root_given == 0)); then
			root="$(env_value PEEN_WORKSPACE_ROOT)"
		fi
	fi
	control="${control:-$CONTROL_SPACE_DOCKER}"
	agent="${agent:-$AGENT_SPACE_DOCKER}"
	case "$control" in "$CONTROL_SPACE_HOST" | "$CONTROL_SPACE_DOCKER") ;; *) fail "PEEN_CONTROL_SPACE in $ENV_FILE must be host or docker" ;; esac

	if ((first && root_given == 0)) && [[ "$control" == "$CONTROL_SPACE_DOCKER" && "$agent" == "$AGENT_SPACE_CONTROL" ]]; then
		fail "--control-space docker --agent-space control needs an explicit --workspace-root, because that directory is everything the agent can read and change"
	fi
	root="${root:-$HOME}"
	[[ "$root" == /* ]] || fail "the workspace root must be an absolute path: $root"
	[[ -d "$root" ]] || fail "the workspace root $root does not exist"
	root="$(canonical_directory "$root")" || fail "cannot enter the workspace root $root"

	validate_combination "$control" "$agent"

	if ((first)); then
		effective_name="${name:-$(default_container_name)}"
	else
		effective_name="${name:-$(container_name)}"
		if [[ "$recorded_control" == "$CONTROL_SPACE_HOST" || "$recorded_control" == "$CONTROL_SPACE_DOCKER" ]] &&
			controller_running &&
			[[ "$control" != "$recorded_control" || "$effective_name" != "$(container_name)" ]]; then
			fail "the controller is running; run peen stop before changing the control space or container name"
		fi
	fi
	if [[ "$control" == "$CONTROL_SPACE_DOCKER" && "$(container_state "$effective_name")" == "foreign" ]]; then
		refuse_foreign_container "$effective_name"
	fi

	image="$(image_reference "$(resolve_tag "$rolling")")"
	say "pinning $image"

	install -d -m 0700 "$PEEN_HOME_DIR" "$PEEN_HOME_DIR/config" "$PEEN_HOME_DIR/state"
	chmod 0700 "$PEEN_HOME_DIR" "$PEEN_HOME_DIR/config" "$PEEN_HOME_DIR/state"

	if [[ "$control" == "$CONTROL_SPACE_DOCKER" || "$agent" == "$AGENT_SPACE_DOCKER" ]]; then
		say "pulling $image"
		docker pull "$image" || fail "could not pull $image"
	fi
	if [[ "$control" == "$CONTROL_SPACE_HOST" ]] && ! binary_matches "$image"; then
		download_binary "$image"
	fi

	agent_space_value="$AGENT_SPACE_CONTROL"
	if [[ "$agent" == "$AGENT_SPACE_DOCKER" ]]; then
		agent_space_value="$AGENT_SPACE_DOCKER_DEFAULT"
	fi

	if ((first)); then
		write_initial_env "$control" "$image" "$root" "$agent_space_value" "${listen:-$DEFAULT_LISTEN}" "$effective_name"
	else
		say "updating $ENV_FILE"
		if ((control_given)); then
			env_set PEEN_CONTROL_SPACE "$control"
		fi
		# A custom default such as docker:ops stays unless --agent-space names
		# a new one.
		if ((agent_given)); then
			env_set PEEN_DEFAULT_AGENT_SPACE "$agent_space_value"
		fi
		if ((root_given)); then
			env_set PEEN_WORKSPACE_ROOT "$root"
		fi
		if [[ -n "$listen" ]]; then
			env_set PEEN_HTTP_LISTEN_ADDRESS "$listen"
		fi
		if [[ -n "$name" ]]; then
			env_set PEEN_CONTAINER_NAME "$name"
		fi
		repin_worker_image "$(env_value PEEN_IMAGE)" "$image"
		env_set PEEN_IMAGE "$image"
		chmod 0600 "$ENV_FILE"
		if controller_running; then
			say "the controller is running; run peen restart to apply the changes"
		fi
	fi

	say "Peen is set up: control space $control, agent space $(env_value PEEN_DEFAULT_AGENT_SPACE), workspace root $(env_value PEEN_WORKSPACE_ROOT)"
	say "configuration: $ENV_FILE"
}

# repin_worker_image moves PEEN_WORKER_IMAGE to the new release only while it
# still names the release this install ran. PEEN_WORKER_IMAGE replaces every
# Docker profile's image, so an operator clears it to let profile images apply,
# and a cleared or changed value must survive setup and upgrade.
repin_worker_image() {
	local previous="$1" image="$2" current
	if ! current="$(env_get PEEN_WORKER_IMAGE)"; then
		return
	fi
	if [[ "$current" != "$previous" ]]; then
		say "kept PEEN_WORKER_IMAGE=${current}, which was changed after setup"
		return
	fi
	env_set PEEN_WORKER_IMAGE "$image"
}

# write_initial_env runs the image's own `peen config init`, so the template
# always matches the release that reads it.
write_initial_env() {
	local control="$1" image="$2" root="$3" agent_space_value="$4" listen="$5" name="$6"
	local -a init_arguments=(
		config init
		--env-file "$ENV_FILE"
		--config-dir "$PEEN_HOME_DIR/config"
		--state-dir "$PEEN_HOME_DIR/state"
		--workspace-root "$root"
		--default-agent-space "$agent_space_value"
		--worker-image "$image"
		--http-listen-address "$listen"
		--host-username "$(host_username)"
		--host-home "$HOME"
		--control-space "$control"
		--image "$image"
		--container-name "$name"
	)

	say "writing $ENV_FILE"
	if [[ "$control" == "$CONTROL_SPACE_DOCKER" ]]; then
		docker run --rm --network none \
			--user "$(id -u):$(id -g)" \
			-v "$PEEN_HOME_DIR:$PEEN_HOME_DIR" \
			"$image" "${init_arguments[@]}" ||
			fail "peen config init failed in $image"
	else
		"$BINARY" "${init_arguments[@]}" || fail "peen config init failed"
	fi
	[[ -f "$ENV_FILE" ]] || fail "peen config init did not write $ENV_FILE"
	chmod 0600 "$ENV_FILE"
}

# install_binary_command is install.sh's hook for --system with the host
# control space. It runs as root, so the binary lands root-owned in
# /usr/local/lib/peen before setup runs as the real user.
install_binary_command() {
	local rolling=0 image
	case "${1:-}" in
	"") ;;
	--rolling) rolling=1 ;;
	*) fail "unknown flag: $1" ;;
	esac
	image="$(image_reference "$(resolve_tag "$rolling")")"
	if binary_matches "$image"; then
		say "$BINARY already comes from $image"
		return
	fi
	download_binary "$image"
}

# controller_alive is the start loop's early exit. For the host it checks only
# that the started PID lives, because until the exec chain finishes its
# command line is still this script's.
controller_alive() {
	local details pid
	if [[ "$(control_space)" == "$CONTROL_SPACE_HOST" ]]; then
		pid="$(<"$(pid_file)")" || return 1
		# A PID that already exited is the "not alive" answer.
		kill -0 "$pid" 2>/dev/null
		return
	fi
	# A container that vanished is the "not alive" answer.
	details="$(docker container inspect -f '{{.State.Running}} {{.RestartCount}}' "$(container_name)" 2>/dev/null)" || return 1
	[[ "$details" == "true 0" ]]
}

wait_healthy() {
	local deadline=$((SECONDS + START_TIMEOUT_SECONDS))
	while ((SECONDS < deadline)); do
		if healthy; then
			return 0
		fi
		if ! controller_alive; then
			return 1
		fi
		sleep 1
	done
	return 1
}

# docker_controller_arguments builds the shared `docker run` flags. Every path
# is mounted at its literal host path, because the host daemon resolves the
# mounts of the worker containers this controller starts.
docker_controller_arguments() {
	local root="$1" configuration state socket socket_group
	configuration="$(config_dir)"
	state="$(state_dir)"
	[[ -d "$configuration" && -d "$state" && -d "$root" ]] ||
		fail "the config, state, and workspace directories must exist: $configuration, $state, $root"

	DOCKER_ARGUMENTS=(
		--user "$(id -u):$(id -g)"
		--env-file "$ENV_FILE"
		-e "PEEN_HTTP_LISTEN_ADDRESS=:$CONTAINER_PORT"
		-p "$(publish_spec)"
		-v "$configuration:$configuration"
		-v "$state:$state"
	)
	# Docker refuses two mounts at one target.
	if [[ "$root" != "$configuration" && "$root" != "$state" ]]; then
		DOCKER_ARGUMENTS+=(-v "$root:$root")
	fi
	DOCKER_ARGUMENTS+=(-w "$root")
	if ! agent_space_is_docker; then
		return
	fi

	require_local_docker_socket
	socket="$(docker_socket)"
	socket_group="$(stat -c '%g' "$socket")"
	if [[ "$socket_group" == "0" ]]; then
		warn "the Docker socket $socket belongs to group 0; the controller gets that group to reach it"
	fi
	DOCKER_ARGUMENTS+=(-v "$socket:$socket" --group-add "$socket_group")
	if [[ "$socket" != "$DEFAULT_DOCKER_SOCKET" ]]; then
		DOCKER_ARGUMENTS+=(-e "PEEN_DOCKER_SOCKET=$socket")
	fi
}

start_docker() {
	local name image root
	name="$(container_name)"
	image="$(pinned_image)"
	require_docker "the docker control space"
	case "$(container_state "$name")" in
	foreign) refuse_foreign_container "$name" ;;
	running)
		say "Peen is already running in container $name"
		return
		;;
	stopped)
		docker rm "$name" >/dev/null || fail "could not remove the stopped container $name"
		;;
	esac

	root="$(workspace_root)"
	docker_controller_arguments "$root"
	say "starting container $name from $image"
	docker run -d --name "$name" --restart unless-stopped \
		--label "$INSTALL_LABEL=$PEEN_HOME_DIR" \
		"${DOCKER_ARGUMENTS[@]}" "$image" run >/dev/null ||
		fail "docker could not start $name"
}

start_host() {
	local root log pid
	[[ -x "$BINARY" ]] || fail "$BINARY is missing; run peen setup"
	if pid="$(host_pid)"; then
		say "Peen is already running (pid $pid)"
		return
	fi
	root="$(workspace_root)"
	[[ -d "$root" ]] || fail "the workspace root $root does not exist"
	log="$(log_file)"
	host_environment
	say "starting $BINARY in $root"
	# The subshell execs through setsid and env, so $! is the controller's own
	# PID and the process leaves this terminal's session.
	(
		cd -- "$root"
		if command -v setsid >/dev/null 2>&1; then
			exec setsid env -i "${HOST_ENVIRONMENT[@]}" "$BINARY" run
		fi
		exec nohup env -i "${HOST_ENVIRONMENT[@]}" "$BINARY" run
	) </dev/null >>"$log" 2>&1 &
	pid=$!
	(
		umask 077
		printf '%s\n' "$pid" >"$(pid_file)"
	)
}

start_command() {
	refuse_root
	require_env_file
	if controller_running && healthy; then
		say "Peen is already running at $(base_url)/"
		return
	fi
	if [[ "$(control_space)" == "$CONTROL_SPACE_DOCKER" ]]; then
		start_docker
	else
		start_host
	fi

	if ! wait_healthy; then
		printf 'error: Peen did not become healthy at %s/healthz\n' "$(base_url)" >&2
		print_log_tail "$LOG_TAIL_ON_FAILURE"
		printf 'hint: check the model provider and its key in %s (peen config edit), then run peen doctor\n' "$ENV_FILE" >&2
		stop_after_failed_start
		exit 1
	fi
	say "Peen is running at $(base_url)/"
}

# stop_after_failed_start leaves no restart loop or half-started process
# behind; the log lines were already printed.
stop_after_failed_start() {
	local name
	if [[ "$(control_space)" == "$CONTROL_SPACE_HOST" ]]; then
		stop_host >/dev/null
		return
	fi
	name="$(container_name)"
	if [[ "$(container_state "$name")" == "foreign" ]]; then
		return
	fi
	if ! docker rm -f "$name" >/dev/null; then
		warn "could not remove the failed container $name"
	fi
}

stop_docker() {
	local name
	name="$(container_name)"
	require_docker "stopping the docker control space"
	case "$(container_state "$name")" in
	absent)
		say "Peen is not running"
		return
		;;
	foreign) refuse_foreign_container "$name" ;;
	esac
	say "stopping container $name"
	docker stop -t "$STOP_TIMEOUT_SECONDS" "$name" >/dev/null || fail "could not stop $name"
	docker rm "$name" >/dev/null || fail "could not remove $name"
}

stop_host() {
	local pid waited=0
	if ! pid="$(host_pid)"; then
		rm -f -- "$(pid_file)"
		say "Peen is not running"
		return
	fi
	say "stopping Peen (pid $pid)"
	kill -TERM "$pid" || fail "could not signal pid $pid"
	# kill -0 reports the exit we are waiting for as an error message.
	while kill -0 "$pid" 2>/dev/null; do
		if ((waited >= STOP_TIMEOUT_SECONDS)); then
			warn "Peen did not stop within ${STOP_TIMEOUT_SECONDS}s; killing it"
			kill -KILL "$pid" || warn "pid $pid exited before it could be killed"
			break
		fi
		sleep 1
		waited=$((waited + 1))
	done
	rm -f -- "$(pid_file)"
}

stop_command() {
	require_env_file
	if [[ "$(control_space)" == "$CONTROL_SPACE_DOCKER" ]]; then
		stop_docker
		return
	fi
	stop_host
}

restart_command() {
	refuse_root
	stop_command
	start_command
}

run_command() {
	local directory="${1:-$PWD}" root
	local -a tty=()
	refuse_root
	require_env_file
	[[ $# -le 1 ]] || fail "peen run takes at most one directory"
	root="$(canonical_directory "$directory")" || fail "$directory is not a directory"
	if controller_running; then
		fail "the managed controller is running; run peen stop first, because both would use $(listen_address)"
	fi

	if [[ "$(control_space)" == "$CONTROL_SPACE_HOST" ]]; then
		[[ -x "$BINARY" ]] || fail "$BINARY is missing; run peen setup"
		host_environment
		say "running Peen in the foreground at $(base_url)/ with workspace root $root (Ctrl-C stops it)"
		cd -- "$root"
		exec env -i "${HOST_ENVIRONMENT[@]}" "PEEN_WORKSPACE_ROOT=$root" "$BINARY" run
	fi

	require_docker "the docker control space"
	if [[ -t 0 ]]; then
		tty=(-it)
	fi
	docker_controller_arguments "$root"
	say "running Peen in the foreground at $(base_url)/ with workspace root $root (Ctrl-C stops it)"
	exec docker run --rm "${tty[@]}" --name "$(container_name)-run" \
		"${DOCKER_ARGUMENTS[@]}" -e "PEEN_WORKSPACE_ROOT=$root" \
		"$(pinned_image)" run
}

json_string() {
	local value="$1"
	value="${value//\\/\\\\}"
	value="${value//\"/\\\"}"
	printf '"%s"' "$value"
}

# api_request calls the local API with the token from .env and prints the HTTP
# status, 000 when nothing answered. The token goes in a header file so it never
# shows in the process list.
api_request() {
	local method="$1" path="$2" body="$3" output="$4" headers="$5" token status
	local -a arguments=(-sS -m "$API_TIMEOUT_SECONDS" -X "$method" -o "$output" -D "$headers" -w '%{http_code}')

	work_dir
	token="$(env_value PEEN_API_TOKEN)"
	if [[ -n "$token" ]]; then
		(
			umask 077
			printf 'Authorization: Bearer %s\n' "$token" >"$WORK_DIR/api-auth"
		)
		arguments+=(-H "@$WORK_DIR/api-auth")
	fi
	if [[ -n "$body" ]]; then
		arguments+=(-H "Content-Type: application/json" --data-binary "@$body")
	fi
	if ! status="$(curl "${arguments[@]}" "$(base_url)$path")"; then
		status="000"
	fi
	printf '%s' "$status"
}

open_browser() {
	local url="$1"
	if [[ "${PEEN_NO_BROWSER:-}" == "1" ]]; then
		return
	fi
	# A browser that fails to launch is not an error: the URL is printed.
	if [[ "$(uname -s)" == "Darwin" ]] && command -v open >/dev/null 2>&1; then
		open "$url" >/dev/null 2>&1 &
	elif [[ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]] && command -v xdg-open >/dev/null 2>&1; then
		xdg-open "$url" >/dev/null 2>&1 &
	fi
}

open_command() {
	local directory="${1:-$PWD}" canonical root_setting root body output headers status session url

	refuse_root
	require_env_file
	[[ $# -le 1 ]] || fail "peen open takes at most one directory"
	canonical="$(canonical_directory "$directory")" || fail "$directory is not a directory"
	if [[ "$canonical" == *[[:cntrl:]]* ]]; then
		fail "the directory path holds a control character"
	fi
	root_setting="$(workspace_root)"
	root="$(canonical_directory "$root_setting")" || fail "the workspace root $root_setting does not exist"
	if [[ "$root" != "/" && "$canonical" != "$root" && "$canonical" != "$root"/* ]]; then
		printf 'error: %s is outside the workspace root %s\n' "$canonical" "$root" >&2
		printf 'To open it, widen the root and restart:\n  peen config set PEEN_WORKSPACE_ROOT %s && peen restart\n' "$canonical" >&2
		exit 1
	fi

	if ! controller_running || ! healthy; then
		start_command
	fi

	work_dir
	body="$WORK_DIR/open.json"
	output="$WORK_DIR/open-response.json"
	headers="$WORK_DIR/open-headers"
	printf '{"workspace":%s}' "$(json_string "$canonical")" >"$body"
	status="$(api_request POST /api/v1/sessions/open "$body" "$output" "$headers")"
	if [[ "$status" != 2* ]]; then
		printf 'error: Peen refused to open %s (HTTP %s)\n' "$canonical" "$status" >&2
		if [[ -s "$output" ]]; then
			cat "$output" >&2
			printf '\n' >&2
		fi
		exit 1
	fi
	session="$(grep -i "^$HEADER_SESSION_ID:" "$headers" | tail -n 1 | cut -d: -f2- | tr -d ' \r')" || session=""
	[[ "$session" =~ $SESSION_ID_PATTERN ]] || fail "Peen answered without a session ID"
	url="$(base_url)/?session=$session"
	say "opened $canonical"
	printf '%s\n' "$url"
	open_browser "$url"
}

status_command() {
	local control running=0 name pid
	require_env_file
	control="$(control_space)"
	printf 'control space:  %s\n' "$control"
	printf 'agent space:    %s\n' "$(env_value PEEN_DEFAULT_AGENT_SPACE)"
	printf 'image:          %s\n' "$(env_value PEEN_IMAGE)"
	printf 'workspace root: %s\n' "$(env_value PEEN_WORKSPACE_ROOT)"
	printf 'url:            %s/\n' "$(base_url)"
	if [[ "$control" == "$CONTROL_SPACE_HOST" ]]; then
		if pid="$(host_pid)"; then
			running=1
			printf 'controller:     running (pid %s, binary %s)\n' "$pid" "$BINARY"
		else
			printf 'controller:     stopped\n'
		fi
	elif docker_works; then
		name="$(container_name)"
		case "$(container_state "$name")" in
		running)
			running=1
			printf 'controller:     running in container %s (%s)\n' "$name" "$(docker container inspect -f '{{.Id}}' "$name" | cut -c1-12)"
			;;
		stopped) printf 'controller:     stopped, container %s exists\n' "$name" ;;
		foreign) printf 'controller:     stopped, a container named %s belongs to something else\n' "$name" ;;
		*) printf 'controller:     stopped\n' ;;
		esac
	else
		printf 'controller:     unknown, Docker does not answer\n'
	fi
	if ((running)); then
		if healthy; then
			printf 'health:         ok\n'
		else
			printf 'health:         not answering %s/healthz\n' "$(base_url)"
		fi
	fi
	if docker_works; then
		printf 'worker containers on this Docker daemon (%s):\n' "$WORKER_LABEL"
		docker ps -a --filter "label=$WORKER_LABEL" --format '  {{.Names}}  {{.Status}}  {{.Image}}'
	fi
	if ((running == 0)); then
		exit "$STATUS_STOPPED"
	fi
}

logs_command() {
	local follow=0 lines="$DEFAULT_LOG_LINES" argument
	local -a docker_arguments
	require_env_file
	for argument in "$@"; do
		case "$argument" in
		-f | --follow) follow=1 ;;
		*)
			[[ "$argument" =~ ^[0-9]+$ ]] || fail "peen logs takes -f and a line count, not $argument"
			lines="$argument"
			;;
		esac
	done
	if [[ "$(control_space)" == "$CONTROL_SPACE_HOST" ]]; then
		[[ -f "$(log_file)" ]] || fail "there is no controller log yet; run peen start"
		if ((follow)); then
			exec tail -n "$lines" -f "$(log_file)"
		fi
		tail -n "$lines" "$(log_file)"
		return
	fi
	[[ "$(container_state "$(container_name)")" != "absent" ]] || fail "there is no controller container; run peen start"
	docker_arguments=(logs --tail "$lines")
	if ((follow)); then
		docker_arguments+=(-f)
	fi
	exec docker "${docker_arguments[@]}" "$(container_name)"
}

# secret_keys prints every setting name whose value must be masked: the API
# token and every provider key named by an apiKeyEnv.
secret_keys() {
	local upstreams
	printf 'PEEN_API_TOKEN\n'
	upstreams="$(env_value PEEN_UPSTREAMS)"
	# grep finding no apiKeyEnv is a valid answer: no provider keys.
	grep -o '"apiKeyEnv"[[:space:]]*:[[:space:]]*"[^"]*"' <<<"$upstreams" |
		sed 's/.*"\([^"]*\)"$/\1/' || true
}

is_secret_key() {
	local key="$1" secrets="$2"
	if [[ "$key" =~ $SECRET_NAME_PATTERN ]]; then
		return 0
	fi
	grep -qxF -- "$key" <<<"$secrets"
}

config_show() {
	local line stripped key value secrets
	require_env_file
	secrets="$(secret_keys)"
	while IFS= read -r line || [[ -n "$line" ]]; do
		stripped="${line#"${line%%[![:space:]]*}"}"
		if [[ "$stripped" == \#* || "$stripped" != *=* ]]; then
			printf '%s\n' "$line"
			continue
		fi
		key="${stripped%%=*}"
		value="${stripped#*=}"
		if [[ -n "$value" && "$value" != "$PLACEHOLDER_VALUE" ]] && is_secret_key "$key" "$secrets"; then
			value="$MASKED_VALUE"
		fi
		printf '%s=%s\n' "$key" "$value"
	done <"$ENV_FILE"
}

remind_restart() {
	if controller_running; then
		say "the controller is running; run peen restart to apply the change"
	fi
}

config_set() {
	local key="$1" value="$2"
	validate_key "$key"
	validate_value "$key" "$value"
	require_env_file
	case "$key" in
	PEEN_CONTROL_SPACE) fail "change the control space with peen setup --control-space host|docker" ;;
	PEEN_IMAGE) fail "change the image with peen upgrade, or peen setup --rolling" ;;
	PEEN_CONTAINER_NAME)
		if controller_running; then
			fail "the controller is running; run peen stop before renaming its container"
		fi
		;;
	esac
	env_set "$key" "$value"
	say "set $key in $ENV_FILE"
	remind_restart
}

config_edit() {
	local -a editor
	require_env_file
	read -ra editor <<<"${VISUAL:-${EDITOR:-vi}}"
	"${editor[@]}" "$ENV_FILE"
	chmod 0600 "$ENV_FILE"
	remind_restart
}

config_command() {
	local subcommand="${1:-show}"
	if (($#)); then
		shift
	fi
	case "$subcommand" in
	edit) config_edit ;;
	show) config_show ;;
	path) printf '%s\n' "$ENV_FILE" ;;
	get)
		[[ $# -eq 1 ]] || fail "usage: peen config get KEY"
		validate_key "$1"
		require_env_file
		env_get "$1" || fail "$1 is not set in $ENV_FILE"
		printf '\n'
		;;
	set)
		[[ $# -eq 2 ]] || fail "usage: peen config set KEY VALUE"
		config_set "$1" "$2"
		;;
	*) fail "unknown config command: $subcommand (edit, show, get, set, path)" ;;
	esac
}

check_ok() { printf 'ok    %s\n' "$*"; }
check_warn() { printf 'warn  %s\n' "$*"; }

check_fail() {
	printf 'FAIL  %s\n' "$*"
	DOCTOR_FAILURES=$((DOCTOR_FAILURES + 1))
}

doctor_mode() {
	local path="$1" want="$2" mode
	if [[ ! -e "$path" ]]; then
		check_fail "$path does not exist; run peen setup"
		return
	fi
	mode="$(stat -c '%a' "$path")"
	if [[ "$mode" == "$want" ]]; then
		check_ok "$path has mode $want"
		return
	fi
	check_fail "$path has mode $mode, want $want (chmod $want $path)"
}

doctor_control_space() {
	local control="$1" image
	case "$control" in
	"$CONTROL_SPACE_HOST")
		check_ok "control space host"
		# Only whether the binary runs matters, not its help text.
		if [[ -x "$BINARY" ]] && "$BINARY" --help >/dev/null 2>&1; then
			check_ok "$BINARY runs"
		else
			check_fail "$BINARY is missing or does not run; run peen setup"
		fi
		;;
	"$CONTROL_SPACE_DOCKER")
		check_ok "control space docker"
		if ! docker_works; then
			check_fail "Docker does not answer 'docker info'"
			return
		fi
		check_ok "Docker answers"
		image="$(env_value PEEN_IMAGE)"
		# A missing image makes inspect print only "No such image".
		if docker image inspect "$image" >/dev/null 2>&1; then
			check_ok "image $image is present"
		else
			check_fail "image $image is missing; run peen setup"
		fi
		;;
	*) check_fail "PEEN_CONTROL_SPACE is '$control', want host or docker; run peen setup" ;;
	esac
}

doctor_agent_space() {
	local control="$1" agent="$2" socket
	case "$agent" in
	"$AGENT_SPACE_CONTROL")
		if [[ "$control" == "$CONTROL_SPACE_HOST" ]] && docker_reachable; then
			check_fail "host control space with the control agent space while your user can reach a Docker daemon: the agent would be host root. Run peen setup --agent-space docker"
			return
		fi
		check_ok "agent space control"
		;;
	docker:*)
		if docker_works && socket="$(docker_socket)" && [[ -S "$socket" ]]; then
			check_ok "agent space $agent can reach the Docker socket $socket"
			return
		fi
		check_fail "agent space $agent needs a local Docker daemon this user can use"
		;;
	"") check_warn "PEEN_DEFAULT_AGENT_SPACE is empty; Peen picks one at startup" ;;
	*) check_fail "PEEN_DEFAULT_AGENT_SPACE is '$agent', want control or docker:<profile>" ;;
	esac
}

doctor_provider_keys() {
	local key value provider_keys placeholders
	provider_keys="$(secret_keys | grep -vx PEEN_API_TOKEN)" || provider_keys=""
	while IFS= read -r key; do
		if [[ -z "$key" ]]; then
			continue
		fi
		value="$(env_value "$key")"
		if [[ -z "$value" || "$value" == "$PLACEHOLDER_VALUE" ]]; then
			check_fail "provider key $key is not set; run peen config set $key <your key>"
			continue
		fi
		check_ok "provider key $key is set"
	done <<<"$provider_keys"

	placeholders="$(grep -E "^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*=$PLACEHOLDER_VALUE\$" "$ENV_FILE" |
		sed 's/=.*//; s/^[[:space:]]*//')" || placeholders=""
	while IFS= read -r key; do
		if [[ -z "$key" ]] || grep -qxF -- "$key" <<<"$provider_keys"; then
			continue
		fi
		check_warn "$key is still $PLACEHOLDER_VALUE; delete it if no upstream uses it"
	done <<<"$placeholders"
}

doctor_controller() {
	local address output headers status
	address="$(listen_address)"
	if ! controller_running; then
		check_fail "the controller is not running; run peen start"
		if port_busy "$address"; then
			check_fail "$address is already in use by another program; pick another with peen setup --listen"
		fi
		return
	fi
	check_ok "the controller is running"
	if healthy; then
		check_ok "$(base_url)/healthz answers"
	else
		check_fail "$(base_url)/healthz does not answer; see peen logs"
	fi
	work_dir
	output="$WORK_DIR/models.json"
	headers="$WORK_DIR/models-headers"
	status="$(api_request GET /api/v1/models "" "$output" "$headers")"
	if [[ "$status" == "200" ]]; then
		check_ok "GET /api/v1/models answers"
		return
	fi
	check_fail "GET /api/v1/models answered HTTP $status; check PEEN_API_TOKEN and the provider settings"
}

doctor_command() {
	local control root
	if [[ ! -f "$ENV_FILE" ]]; then
		check_fail "$ENV_FILE does not exist; run peen setup"
		exit 1
	fi
	doctor_mode "$ENV_FILE" 600
	doctor_mode "$PEEN_HOME_DIR" 700
	doctor_mode "$(config_dir)" 700
	doctor_mode "$(state_dir)" 700

	control="$(env_value PEEN_CONTROL_SPACE)"
	doctor_control_space "$control"
	doctor_agent_space "$control" "$(env_value PEEN_DEFAULT_AGENT_SPACE)"
	doctor_provider_keys

	root="$(env_value PEEN_WORKSPACE_ROOT)"
	if [[ -n "$root" && -d "$root" ]]; then
		check_ok "workspace root $root exists"
	else
		check_fail "workspace root '$root' does not exist"
	fi

	if [[ "$control" == "$CONTROL_SPACE_HOST" || "$control" == "$CONTROL_SPACE_DOCKER" ]]; then
		doctor_controller
	fi

	if ((DOCTOR_FAILURES > 0)); then
		printf '%s check(s) failed\n' "$DOCTOR_FAILURES"
		exit 1
	fi
	printf 'all checks passed\n'
}

refresh_wrapper() {
	local reference="$1" url downloaded
	url="$SOURCE_URL/$reference/scripts/peen.sh"
	work_dir
	downloaded="$WORK_DIR/peen.sh"
	say "refreshing $WRAPPER_PATH from $url"
	curl -fsSL -o "$downloaded" "$url" || fail "could not download $url"
	[[ "$(sed -n 2p "$downloaded")" == "$MARKER" ]] || fail "$url is not a managed peen command"
	bash -n "$downloaded" || fail "$url is not valid bash"
	install_file "$downloaded" "$WRAPPER_PATH" 0755
}

upgrade_command() {
	local rolling=0 tag image old_image control was_running=0 docker_used=0 reference
	case "${1:-}" in
	"") ;;
	--rolling) rolling=1 ;;
	*) fail "unknown upgrade flag: $1" ;;
	esac
	refuse_root
	require_env_file
	control="$(control_space)"
	old_image="$(env_value PEEN_IMAGE)"
	if [[ "$control" == "$CONTROL_SPACE_DOCKER" ]] || agent_space_is_docker; then
		docker_used=1
		require_docker "the upgrade"
	fi
	if controller_running; then
		was_running=1
	fi

	tag="$(resolve_tag "$rolling")"
	image="$(image_reference "$tag")"
	reference="$tag"
	if ((rolling)); then
		reference="$ROLLING_SOURCE_REF"
	fi
	say "upgrading from ${old_image:-nothing} to $image"

	if ((docker_used)); then
		docker pull "$image" || fail "could not pull $image"
	fi
	if [[ "$control" == "$CONTROL_SPACE_HOST" ]]; then
		download_binary "$image"
	fi
	refresh_wrapper "$reference"
	repin_worker_image "$old_image" "$image"
	env_set PEEN_IMAGE "$image"

	if ((was_running)); then
		say "restarting Peen on $image"
		if ! PEEN_HOME="$PEEN_HOME_DIR" "$WRAPPER_PATH" restart; then
			printf 'error: Peen %s did not start. Before 1.0 a release can refuse a database an older release created; the controller lines above name the files to delete. See "Durable state and mounts" in docs/deployment.md.\n' "$tag" >&2
			exit 1
		fi
	fi

	if ((docker_used)) && [[ -n "$old_image" && "$old_image" != "$image" ]]; then
		# docker's own message for an image in use adds nothing to the warning.
		if docker image rm "$old_image" >/dev/null 2>&1; then
			say "removed the previous image $old_image"
		else
			warn "kept the previous image $old_image, it is still in use"
		fi
	fi
	say "Peen is on $image"
}

# elevated runs a removal through sudo when PATH's directory is not writable,
# as for a --system install.
elevated_for() {
	local path="$1"
	shift
	if [[ -w "$(dirname -- "$path")" ]]; then
		"$@"
		return
	fi
	command -v sudo >/dev/null 2>&1 || fail "changing $path needs root and sudo is not installed"
	sudo "$@"
}

remove_path() {
	local path="$1"
	if [[ ! -e "$path" && ! -L "$path" ]]; then
		return
	fi
	elevated_for "$path" rm -f -- "$path"
}

remove_empty_directory() {
	local path="$1"
	if [[ ! -d "$path" ]]; then
		return
	fi
	# rmdir's "not empty" message is replaced by the warning.
	if ! elevated_for "$path" rmdir -- "$path" 2>/dev/null; then
		warn "kept $path, it is not empty"
	fi
}

ask_delete_data() {
	local answer=""
	# Opening /dev/tty fails without a controlling terminal, as under a pipe
	# from curl, and the message below says what to do instead.
	if ! (: </dev/tty) 2>/dev/null; then
		fail "no terminal to ask on; pass --keep-data or --delete-data"
	fi
	printf 'Also delete the configuration and state in %s? [y/N] ' "$PEEN_HOME_DIR" >/dev/tty
	read -r answer </dev/tty || answer=""
	case "$answer" in
	y | Y | yes | YES) printf '1' ;;
	*) printf '0' ;;
	esac
}

uninstall_stop() {
	local control name image
	control="$(env_value PEEN_CONTROL_SPACE)"
	if [[ "$control" == "$CONTROL_SPACE_HOST" ]]; then
		stop_host
	elif [[ "$control" == "$CONTROL_SPACE_DOCKER" ]] && docker_works; then
		name="$(container_name)"
		case "$(container_state "$name")" in
		running | stopped)
			say "removing container $name"
			docker rm -f "$name" >/dev/null || warn "could not remove $name"
			;;
		foreign) warn "left the container $name alone, it does not belong to this installation" ;;
		esac
	fi

	image="$(env_value PEEN_IMAGE)"
	if [[ -z "$image" ]] || ! docker_works; then
		return
	fi
	# docker's own message for a missing or used image adds nothing.
	if docker image rm "$image" >/dev/null 2>&1; then
		say "removed the image $image"
	else
		warn "kept the image $image, it is missing or in use"
	fi
}

uninstall_command() {
	local delete_data="" canonical_home canonical_user_home

	case "${1:-}" in
	--keep-data) delete_data=0 ;;
	--delete-data) delete_data=1 ;;
	"") ;;
	*) fail "unknown uninstall flag: $1 (--keep-data or --delete-data)" ;;
	esac
	if [[ -z "$delete_data" ]]; then
		delete_data="$(ask_delete_data)"
	fi

	if [[ -f "$ENV_FILE" ]]; then
		uninstall_stop
	fi

	say "removing $WRAPPER_PATH"
	remove_path "$WRAPPER_PATH"
	remove_path "$BINARY"
	remove_path "$BINARY_IMAGE_FILE"
	remove_empty_directory "$LIB_DIR"

	if ((delete_data == 0)); then
		say "kept your configuration and state in $PEEN_HOME_DIR"
		say "Peen is uninstalled"
		return
	fi
	[[ -f "$ENV_FILE" ]] || fail "$PEEN_HOME_DIR holds no .env, so I will not delete it"
	canonical_home="$(canonical_directory "$PEEN_HOME_DIR")" || fail "cannot enter $PEEN_HOME_DIR"
	canonical_user_home="$(canonical_directory "$HOME")" || canonical_user_home="$HOME"
	if [[ "$canonical_home" == "/" || "$canonical_home" == "$canonical_user_home" ]]; then
		fail "refusing to delete $canonical_home"
	fi
	rm -r -- "$canonical_home"
	say "deleted $canonical_home"
	say "Peen is uninstalled"
}

# server_command runs the Peen server binary's own client commands, `session`
# and `control`, with the controller's configuration: the host binary with the
# same environment start uses, or the binary inside the controller container.
# `session` starts the managed controller first, so the binary never starts an
# unmanaged one of its own. `control status` only reports and starts nothing.
server_command() {
	local command="$1" name
	local -a tty=()
	shift
	refuse_root
	require_env_file
	if [[ "$command" == "$SERVER_COMMAND_SESSION" ]] && ! { controller_running && healthy; }; then
		start_command
	fi

	if [[ "$(control_space)" == "$CONTROL_SPACE_HOST" ]]; then
		[[ -x "$BINARY" ]] || fail "$BINARY is missing; run peen setup"
		host_environment
		exec env -i "${HOST_ENVIRONMENT[@]}" "$BINARY" "$command" "$@"
	fi

	require_docker "the docker control space"
	name="$(container_name)"
	case "$(container_state "$name")" in
	running) ;;
	foreign) refuse_foreign_container "$name" ;;
	*)
		# Matches what the binary's own `control status` prints for a
		# controller it cannot reach. `session` started one above.
		printf 'controller: not running\n'
		return
		;;
	esac
	if [[ -t 0 && -t 1 ]]; then
		tty=(-t)
	fi
	exec docker exec -i "${tty[@]}" "$name" "$CONTAINER_BINARY" "$command" "$@"
}

version_command() {
	printf 'command:       %s\n' "$WRAPPER_PATH"
	if [[ -f "$ENV_FILE" ]]; then
		printf 'image:         %s\n' "$(env_value PEEN_IMAGE)"
		printf 'control space: %s\n' "$(env_value PEEN_CONTROL_SPACE)"
	fi
	if [[ -f "$BINARY_IMAGE_FILE" ]]; then
		printf 'binary:        %s from %s\n' "$BINARY" "$(<"$BINARY_IMAGE_FILE")"
	fi
}

main() {
	local command
	work_dir
	resolve_paths
	if (($# == 0)); then
		open_command
		return
	fi
	command="$1"
	shift
	case "$command" in
	setup) setup "$@" ;;
	start)
		[[ $# -eq 0 ]] || fail "peen start takes no arguments"
		start_command
		;;
	stop) stop_command ;;
	restart) restart_command ;;
	status) status_command ;;
	logs) logs_command "$@" ;;
	open) open_command "$@" ;;
	run) run_command "$@" ;;
	config) config_command "$@" ;;
	doctor) doctor_command ;;
	"$SERVER_COMMAND_SESSION" | "$SERVER_COMMAND_CONTROL") server_command "$command" "$@" ;;
	upgrade) upgrade_command "$@" ;;
	uninstall) uninstall_command "$@" ;;
	version | --version) version_command ;;
	help | --help | -h) usage ;;
	__install-binary) install_binary_command "$@" ;;
	. | .. | */*)
		[[ $# -eq 0 ]] || fail "peen <dir> takes one directory"
		open_command "$command"
		;;
	*) fail "unknown command: $command (see peen help)" ;;
	esac
}

main "$@"
