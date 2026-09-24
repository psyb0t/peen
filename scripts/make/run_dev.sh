#!/bin/bash
set -euo pipefail

readonly log_file="${LOG_FILE:-/tmp/peen-run-dev.log}"
exec > >(tee -a "$log_file") 2>&1

trap 'log ERROR "command failed exit=$?"' ERR

json_string() {
	local value="$1"
	value="${value//\\/\\\\}"
	value="${value//\"/\\\"}"
	value="${value//$'\n'/\\n}"
	value="${value//$'\r'/\\r}"
	value="${value//$'\t'/\\t}"
	printf '"%s"' "$value"
}

log() {
	local level="$1"
	shift
	local timestamp file line function_name message
	timestamp="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	file="${BASH_SOURCE[1]##*/}"
	line="${BASH_LINENO[0]}"
	function_name="${FUNCNAME[1]:-main}"
	message="$*"
	printf '{"time":%s,"level":%s,"file":%s,"line":%d,"func":%s,"msg":%s}\n' \
		"$(json_string "$timestamp")" \
		"$(json_string "$level")" \
		"$(json_string "$file")" \
		"$line" \
		"$(json_string "$function_name")" \
		"$(json_string "$message")" >&2
}

ensure_directory() {
	local path="$1"
	if [[ -e "$path" ]]; then
		[[ -d "$path" ]] || {
			log ERROR "expected a directory path=$path"
			exit 1
		}
		return
	fi
	mkdir -p -- "$path"
}

peen_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
workspace_root="$(pwd -P)"
environment_file="$peen_root/.env"
configuration_directory="$peen_root/data/peen/config"
state_directory="$peen_root/data/peen/state"
http_port="${PEEN_DEV_HTTP_PORT:-8080}"
host_uid="$(id -u)"
host_gid="$(id -g)"
host_username="$(id -un)"
host_home="${HOME:-}"

[[ -f "$environment_file" ]] || {
	log ERROR "missing .env, copy .env.example and configure a provider first"
	exit 1
}
[[ "$host_home" == /* ]] || {
	log ERROR "HOME must be an absolute path"
	exit 1
}
if ! [[ "$http_port" =~ ^[0-9]{1,5}$ ]] || ((http_port < 1 || http_port > 65535)); then
	log ERROR "PEEN_DEV_HTTP_PORT must be an integer from 1 through 65535"
	exit 1
fi

ensure_directory "$configuration_directory"
ensure_directory "$state_directory"

log INFO "building the development image"
make -C "$peen_root" dev-image

workspace_roots="[$(json_string "$workspace_root")]"
docker_args=(
	--rm
	--init
	--network host
	--user "$host_uid:$host_gid"
	--env-file "$environment_file"
	--workdir "$workspace_root"
	--env "HOME=/tmp"
	--env "GOCACHE=/tmp/go-cache"
	--env "GOMODCACHE=/tmp/go-mod-cache"
	--env "PEEN_DEV_SOURCE_ROOT=$peen_root"
	--env "PEEN_CONFIG_DIR=$configuration_directory"
	--env "PEEN_STATE_DIR=$state_directory"
	--env "PEEN_WORKSPACE_ROOTS=$workspace_roots"
	--env "PEEN_HTTP_LISTEN_ADDRESS=127.0.0.1:$http_port"
	--env "PEEN_HOST_USERNAME=$host_username"
	--env "PEEN_HOST_HOME=$host_home"
)

if [[ "$peen_root" != "$workspace_root" && "$peen_root" != "$workspace_root/"* ]]; then
	docker_args+=(--volume "$peen_root:$peen_root:ro")
fi
docker_args+=(
	--volume "$workspace_root:$workspace_root"
	--volume "$configuration_directory:$configuration_directory:ro"
	--volume "$state_directory:$state_directory"
)

case "${PEEN_DEV_DOCKER_SOCKET:-0}" in
0 | "") ;;
1)
	docker_socket="${PEEN_DOCKER_SOCKET:-/var/run/docker.sock}"
	[[ -S "$docker_socket" ]] || {
		log ERROR "PEEN_DOCKER_SOCKET must name an existing Unix socket"
		exit 1
	}
	socket_gid="$(stat -c '%g' "$docker_socket")"
	docker_args+=(
		--group-add "$socket_gid"
		--volume "$docker_socket:$docker_socket"
		--env "PEEN_DOCKER_SOCKET=$docker_socket"
	)
	log WARN "Docker socket access is enabled for this development controller"
	;;
*)
	log ERROR "PEEN_DEV_DOCKER_SOCKET must be 0 or 1"
	exit 1
	;;
esac

log INFO "starting development controller network=host url=http://localhost:$http_port"
exec docker run "${docker_args[@]}" peen-dev \
	bash -ceu 'CGO_ENABLED=0 go build -o /tmp/peen "$PEEN_DEV_SOURCE_ROOT/cmd"; unset PEEN_DEV_SOURCE_ROOT; exec /tmp/peen run'
