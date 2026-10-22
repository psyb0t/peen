#!/bin/bash
# Runs the public Peen action. The caller owns checkout and publication.
set -euo pipefail

log() {
	local level="$1"
	shift
	jq -nc --arg level "$level" --arg msg "$*" \
		--arg time "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
		--arg file "${BASH_SOURCE[1]##*/}" --argjson line "${BASH_LINENO[0]}" \
		--arg func "${FUNCNAME[1]:-main}" \
		'{time:$time,level:$level,file:$file,line:$line,func:$func,msg:$msg}' >&2
}
trap 'log ERROR "action failed exit=$?"' ERR
fail() {
	log ERROR "$*"
	exit 1
}
absolute() {
	case "$1" in
	/*) printf '%s' "$1" ;;
	*) printf '%s/%s' "$GITHUB_WORKSPACE" "$1" ;;
	esac
}

[[ "${RUNNER_OS:-}" == Linux ]] || fail 'Peen requires a Linux runner with Docker'
: "${GITHUB_WORKSPACE:?}" "${RUNNER_TEMP:?}" "${GITHUB_OUTPUT:?}"
[[ -n "${IN_IMAGE:-}" && "$IN_IMAGE" != -* ]] || fail 'image is required'
[[ -n "${IN_UPSTREAMS:-}" && -n "${IN_MODEL:-}" ]] || fail 'upstreams and model are required'
[[ -z "${IN_PROMPT:-}" || -z "${IN_PROMPT_FILE:-}" ]] || fail 'set prompt or prompt-file, not both'
if [[ -n "${IN_PROMPT_FILE:-}" ]]; then
	prompt_file="$(absolute "$IN_PROMPT_FILE")"
	[[ -f "$prompt_file" && -s "$prompt_file" ]] || fail 'prompt-file must be a nonempty file'
elif [[ -n "${IN_PROMPT:-}" ]]; then
	prompt_file=''
else
	fail 'set prompt or prompt-file'
fi
workspace="$(cd -- "$(absolute "${IN_WORKING_DIRECTORY:-.}")" && pwd -P)"
[[ -d "$workspace" && "$workspace" != *:* && "$workspace" != *,* ]] || fail 'working-directory must be a directory without colons or commas'
[[ "$workspace" != / && "$workspace" != /peen-action && "$workspace" != /peen-action/* ]] || fail 'working-directory overlaps action runtime paths'
[[ "$(id -u)" != 0 ]] || fail 'run the action as a non-root runner user'
jq -e 'type == "array" and length > 0 and all(.[]; type == "object")' \
	<<<"$IN_UPSTREAMS" >/dev/null || fail 'upstreams must be a nonempty JSON array of objects'
credential_names="$(jq -r '.[] | .apiKeyEnv // empty' <<<"$IN_UPSTREAMS")"
credential_args=()
while IFS= read -r key; do
	[[ -n "$key" ]] || continue
	[[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || fail 'apiKeyEnv must name an environment variable'
	[[ -n "${!key:-}" ]] || fail "missing provider credential variable: $key"
	credential_args+=(--env "$key")
done <<<"$credential_names"

umask 077
runtime="$(mktemp -d "$RUNNER_TEMP/peen-action.XXXXXXXX")"
mkdir -p "$runtime/config" "$runtime/state" "$runtime/home"
readonly LOG_FILE="$runtime/action.log"
# Preserve separate streams: stdout is the reply, stderr is diagnostics.
exec 2> >(tee -a "$LOG_FILE" >&2)
container_name="peen-action-$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
started=false
# shellcheck disable=SC2329 # Bash invokes cleanup through the EXIT trap.
cleanup() {
	if [[ "$started" == true ]]; then
		if ! docker rm -f "$container_name" >/dev/null; then
			log WARN 'could not remove the action container'
		fi
	fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export PEEN_UPSTREAMS="$IN_UPSTREAMS" PEEN_DEFAULT_MODEL="$IN_MODEL"
export PEEN_COMPACTION_MODEL="${IN_COMPACTION_MODEL:-$IN_MODEL}"
args=(exec --workspace "$workspace" --agent-space control --output text --prompt -)
[[ -z "${IN_REASONING_EFFORT:-}" ]] || args+=(--reasoning-effort "$IN_REASONING_EFFORT")
[[ -z "${IN_SYSTEM_PROMPT:-}" ]] || args+=(--system-prompt "$IN_SYSTEM_PROMPT")
result_file="$runtime/result.txt"
log INFO 'pulling Peen image'
docker pull --quiet "$IN_IMAGE" || fail 'could not pull Peen image'
log INFO 'running Peen prompt'
# Create before marking ownership, so cleanup never targets a preexisting container.
docker create --name "$container_name" --interactive \
	--user "$(id -u):$(id -g)" \
	--volume "$workspace:$workspace" --workdir "$workspace" \
	--volume "$runtime:/peen-action" \
	--env HOME=/peen-action/home --env "USER=$(id -un)" \
	--env PEEN_CONFIG_DIR=/peen-action/config \
	--env PEEN_STATE_DIR=/peen-action/state \
	--env PEEN_WORKER_SOCKET_DIR=/tmp/peen-workers \
	--env "PEEN_WORKSPACE_ROOT=$workspace" \
	--env PEEN_DEFAULT_AGENT_SPACE=control \
	--env PEEN_UPSTREAMS --env PEEN_DEFAULT_MODEL --env PEEN_COMPACTION_MODEL \
	"${credential_args[@]}" "$IN_IMAGE" "${args[@]}" >/dev/null || fail 'could not create Peen container'
started=true
status=0
if [[ -n "$prompt_file" ]]; then
	docker start --attach --interactive "$container_name" <"$prompt_file" | tee "$result_file" || status=$?
else
	printf '%s' "$IN_PROMPT" | docker start --attach --interactive "$container_name" | tee "$result_file" || status=$?
fi
# A random delimiter keeps multiline replies from injecting output entries.
delimiter="PEEN_$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
{
	printf 'result-file=%s\nresult<<%s\n' "$result_file" "$delimiter"
	cat "$result_file"
	printf '\n%s\n' "$delimiter"
} >>"$GITHUB_OUTPUT"
exit "$status"
