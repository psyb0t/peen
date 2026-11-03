#!/bin/bash
# Run Peen against coding benchmarks in a networkless sandbox.
#
# The Makefile's bench-* targets call this with one command. Everything the
# runs produce lives under the gitignored .testing/bench at its host path,
# because Harbor starts each task container on the host's Docker daemon and
# binds those paths into it. docs/benchmarks.md explains the setup.
#
#   image       build the Harbor driver image from bench/
#   build       build static peen and relay binaries from this checkout
#   datasets    download Aider Polyglot and the pinned LHTB tasks
#   proxy       start the provider proxy, which alone reads the key from .env
#   proxy-stop  stop the provider proxy this script started
#   run SUITE   write a job for SUITE (aider, lhtb, eval, or evolve) and run it with Harbor

set -euo pipefail

log_file="${LOG_FILE:-/tmp/peen-bench.log}"

readonly DRIVER_IMAGE="peen-bench-driver:local"
readonly DEV_IMAGE="peen-dev"
readonly PROXY_CONTAINER="peen-bench-proxy"
readonly RUN_CONTAINER_PREFIX="peen-bench-run-"
readonly DOCKER_SOCKET="/var/run/docker.sock"
readonly PROVIDER_SOCKET_NAME="provider.sock"
readonly AIDER_DATASET="aider-polyglot@1.0"
readonly LHTB_REPOSITORY="https://github.com/zli12321/lhtb.git"
readonly LHTB_COMMIT="7ca0cacea9bf6a0694eaad7306bed794eaaaabce"
readonly LHTB_TASKS="langchain-version-migration rush_hour_campaign snake_maze_campaign sudoku-recovery"
readonly AGENT_PEEN="peen"
readonly PROXY_READY_SECONDS=30
readonly PROXY_MEMORY="512m"
readonly PROXY_PIDS=64
readonly NAME_PATTERN='^[A-Za-z0-9._-]+$'
readonly TASKS_PATTERN='^[A-Za-z0-9._ -]*$'
readonly MODEL_PATTERN='^[A-Za-z0-9./_-]*$'
readonly NUMBER_PATTERN='^[0-9]*$'

log() {
	local level="$1"
	shift

	jq -nc \
		--arg time "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')" \
		--arg level "${level}" \
		--arg file "${BASH_SOURCE[1]##*/}" \
		--argjson line "${BASH_LINENO[0]}" \
		--arg func "${FUNCNAME[1]:-main}" \
		--arg msg "$*" \
		'{time:$time,level:$level,file:$file,line:$line,func:$func,msg:$msg}' >&2
}

on_error() {
	local exit_code=$?
	log ERROR "bench command failed exit=${exit_code}"
	exit "${exit_code}"
}

trap on_error ERR
exec > >(tee -a "${log_file}") 2>&1

# Docker binds host paths, so every path handed to a container is under
# repo_root, the checkout's host path. The local_ paths are the same
# directories as this shell sees them. They differ only when make itself runs
# inside a container that mounts the checkout somewhere else.
local_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
repo_root="${BENCH_REPO_ROOT:-${local_root}}"
source_dir="${repo_root}/bench"
state_dir="${repo_root}/.testing/bench"
bin_dir="${state_dir}/bin"
local_state_dir="${local_root}/.testing/bench"
local_bin_dir="${local_state_dir}/bin"
local_provider_socket="${local_state_dir}/provider/${PROVIDER_SOCKET_NAME}"

require_pattern() {
	local name="$1"
	local value="$2"
	local pattern="$3"

	if [[ ! "${value}" =~ ${pattern} ]]; then
		log ERROR "${name} has characters outside ${pattern}"
		exit 1
	fi
}

require_image() {
	if ! docker image inspect "${DRIVER_IMAGE}" >/dev/null 2>&1; then
		log ERROR "missing ${DRIVER_IMAGE}, run make bench-image"
		exit 1
	fi
}

# driver runs the Harbor driver image as the caller, with the bench state
# writable and the bench sources read-only.
driver() {
	docker run --rm \
		--user "$(id -u):$(id -g)" \
		-v "${state_dir}:${state_dir}" \
		-v "${source_dir}:${source_dir}:ro" \
		-w "${state_dir}" \
		"$@"
}

build_image() {
	docker build --tag "${DRIVER_IMAGE}" "${source_dir}"
	log INFO "built ${DRIVER_IMAGE}"
}

build_binaries() {
	local version

	version="$(git -C "${local_root}" describe --tags --always --dirty)"
	mkdir -p "${local_bin_dir}"
	make -C "${local_root}" dev-image CURDIR="${repo_root}"
	docker run --rm \
		--user "$(id -u):$(id -g)" \
		-e HOME=/tmp \
		-e GOCACHE=/tmp/go-cache \
		-e GOMODCACHE=/tmp/go-mod-cache \
		-e CGO_ENABLED=0 \
		-e BUILD_VERSION="${version}" \
		-e BIN_DIR="${bin_dir}" \
		-v "${repo_root}:${repo_root}" \
		-w "${repo_root}" \
		"${DEV_IMAGE}" \
		sh -ceu '
			go build -trimpath \
				-ldflags "-s -w -X main.buildVersion=${BUILD_VERSION}" \
				-o "${BIN_DIR}/peen" ./cmd
			go build -trimpath -ldflags "-s -w" \
				-o "${BIN_DIR}/relay" ./cmd/benchrelay
		'
	printf '%s\n' "${version}" >"${local_bin_dir}/peen.version"
	log INFO "built peen ${version} and relay into ${bin_dir}"
}

download_datasets() {
	local lhtb_source="${state_dir}/sources/lhtb"
	local local_lhtb_source="${local_state_dir}/sources/lhtb"
	local task

	require_image
	mkdir -p "${local_state_dir}/datasets/lhtb" "${local_state_dir}/sources"
	driver "${DRIVER_IMAGE}" \
		harbor download "${AIDER_DATASET}" -o "${state_dir}/datasets"

	if [[ ! -d "${local_lhtb_source}/.git" ]]; then
		driver "${DRIVER_IMAGE}" \
			git clone --quiet "${LHTB_REPOSITORY}" "${lhtb_source}"
	fi

	driver "${DRIVER_IMAGE}" git -C "${lhtb_source}" fetch --quiet origin
	driver "${DRIVER_IMAGE}" \
		git -C "${lhtb_source}" checkout --quiet --detach "${LHTB_COMMIT}"

	for task in ${LHTB_TASKS}; do
		if [[ -e "${local_state_dir}/datasets/lhtb/${task}" ]]; then
			log INFO "LHTB task ${task} already present"
			continue
		fi
		cp -R "${local_lhtb_source}/tasks/${task}" "${local_state_dir}/datasets/lhtb/"
	done

	log INFO "datasets ready under ${state_dir}/datasets"
}

start_proxy() {
	local waited=0

	require_image
	if [[ ! -f "${local_root}/.env" ]]; then
		log ERROR "missing .env, the proxy reads the provider key from it"
		exit 1
	fi

	if docker container inspect "${PROXY_CONTAINER}" >/dev/null 2>&1; then
		log ERROR "${PROXY_CONTAINER} already exists, run make bench-proxy-stop"
		exit 1
	fi

	if [[ -e "${local_provider_socket}" ]]; then
		log ERROR "${local_provider_socket} is left from an earlier proxy, remove it first"
		exit 1
	fi

	mkdir -p "${local_state_dir}/provider"
	docker run --detach --init \
		--name "${PROXY_CONTAINER}" \
		--user "$(id -u):$(id -g)" \
		--cap-drop ALL \
		--security-opt no-new-privileges:true \
		--read-only \
		--memory "${PROXY_MEMORY}" \
		--pids-limit "${PROXY_PIDS}" \
		-e BENCH_STATE_DIR="${state_dir}" \
		-e BENCH_KEY_ENV \
		-e BENCH_BASE_URL \
		-e BENCH_MODELS \
		-v "${state_dir}:${state_dir}" \
		-v "${source_dir}:${source_dir}:ro" \
		-v "${repo_root}/.env:/repo.env:ro" \
		"${DRIVER_IMAGE}" \
		python "${source_dir}/proxy.py" >/dev/null

	while [[ ! -S "${local_provider_socket}" ]]; do
		if ((waited >= PROXY_READY_SECONDS)); then
			log ERROR "proxy socket did not appear, see docker logs ${PROXY_CONTAINER}"
			exit 1
		fi
		sleep 1
		waited=$((waited + 1))
	done

	log INFO "proxy ready, requests are logged to ${state_dir}/proxy.log"
}

stop_proxy() {
	if ! docker container inspect "${PROXY_CONTAINER}" >/dev/null 2>&1; then
		log INFO "${PROXY_CONTAINER} is not running"
		return
	fi

	docker stop "${PROXY_CONTAINER}" >/dev/null
	docker rm "${PROXY_CONTAINER}" >/dev/null
	log INFO "stopped ${PROXY_CONTAINER}"
}

run_suite() {
	local suite="$1"
	local agent="${BENCH_AGENT:-${AGENT_PEEN}}"
	local job="${BENCH_JOB:-${suite}-${agent}-$(date -u '+%Y%m%d-%H%M%S')}"
	local config
	local socket_group

	require_pattern BENCH_JOB "${job}" "${NAME_PATTERN}"
	require_pattern BENCH_AGENT "${agent}" "${NAME_PATTERN}"
	require_pattern BENCH_TASKS "${BENCH_TASKS:-}" "${TASKS_PATTERN}"
	require_pattern BENCH_MODEL "${BENCH_MODEL:-}" "${MODEL_PATTERN}"
	require_pattern BENCH_COMPACTION_MODEL "${BENCH_COMPACTION_MODEL:-}" "${MODEL_PATTERN}"
	require_pattern BENCH_CONTEXT_TOKENS "${BENCH_CONTEXT_TOKENS:-}" "${NUMBER_PATTERN}"
	require_pattern BENCH_CONCURRENCY "${BENCH_CONCURRENCY:-}" "${NUMBER_PATTERN}"
	require_image

	if [[ "${agent}" == "${AGENT_PEEN}" ]]; then
		if [[ ! -x "${local_bin_dir}/peen" || ! -x "${local_bin_dir}/relay" ]]; then
			log ERROR "missing binaries in ${bin_dir}, run make bench-build"
			exit 1
		fi
		if [[ ! -S "${local_provider_socket}" ]]; then
			log ERROR "provider socket missing, run make bench-proxy"
			exit 1
		fi
	fi

	config="$(driver \
		-e BENCH_SUITE="${suite}" \
		-e BENCH_STATE_DIR="${state_dir}" \
		-e BENCH_SOURCE_DIR="${source_dir}" \
		-e BENCH_JOB="${job}" \
		-e BENCH_AGENT="${agent}" \
		-e BENCH_TASKS \
		-e BENCH_MODEL \
		-e BENCH_CONTEXT_TOKENS \
		-e BENCH_COMPACTION_MODEL \
		-e BENCH_CONCURRENCY \
		"${DRIVER_IMAGE}" python "${source_dir}/job.py")"

	socket_group="$(stat -c '%g' "${DOCKER_SOCKET}")"
	log INFO "running job ${job} from ${config}"
	driver \
		--name "${RUN_CONTAINER_PREFIX}${job}" \
		--group-add "${socket_group}" \
		-e PYTHONPATH="${source_dir}" \
		-e BENCH_BIN_DIR="${bin_dir}" \
		-v "${DOCKER_SOCKET}:${DOCKER_SOCKET}" \
		"${DRIVER_IMAGE}" harbor run --config "${config}"
	log INFO "job ${job} finished, trials are under ${state_dir}/jobs/${job}"
}

command="${1:-}"
case "${command}" in
image) build_image ;;
build) build_binaries ;;
datasets) download_datasets ;;
proxy) start_proxy ;;
proxy-stop) stop_proxy ;;
run)
	case "${2:-}" in
	aider | lhtb | eval | evolve) run_suite "$2" ;;
	*)
		log ERROR "run needs a suite: aider, lhtb, eval, or evolve"
		exit 1
		;;
	esac
	;;
*)
	log ERROR "unknown bench command: ${command}"
	exit 1
	;;
esac
