#!/bin/bash
# Run Peen against coding benchmarks in a networkless sandbox.
#
# The Makefile's bench-* targets call this with one command. Everything the
# runs produce lives under the gitignored .testing/bench at its host path,
# because Harbor starts each task container on the host's Docker daemon and
# binds those paths into it. docs/benchmarks.md explains the setup.
#
#   image       build the Harbor driver image from bench/
#   lock        compile bench/requirements.in into the hashed requirements.txt
#   build       build static peen, relay and scenario driver binaries from
#               this checkout, or from BENCH_REF into its own directory
#   datasets    download Aider Polyglot and the pinned LHTB tasks
#   proxy       start the provider proxy, which alone reads the key from .env
#   proxy-stop  stop the provider proxy this script started
#   run SUITE   write a job for SUITE (aider, lhtb, eval, evolve, or harness),
#               run it with Harbor, then audit every harness trial
#   audit JOB   audit a finished harness job's trials again
#   report JOB  write a finished harness job's report
#   compare A B compare two finished harness jobs task by task
#   selftest    run the harness audit library's own tests

set -euo pipefail

log_file="${LOG_FILE:-/tmp/peen-bench.log}"

readonly DRIVER_IMAGE="peen-bench-driver:local"
readonly DEV_IMAGE="peen-dev"
readonly UV_IMAGE="ghcr.io/astral-sh/uv:0.13.6-python3.12-trixie-slim@sha256:bf889bda77474aa50c351caf1538b855ef2b7a30add45f26ca50dcb6ade06402"
readonly BENCH_PYTHON_VERSION="3.12"
readonly MIN_RELEASE_AGE_DAYS=7
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
readonly REF_PATTERN='^[A-Za-z0-9._/-]*$'
readonly TIER_PATTERN='^(smoke|full|chaos)?$'
readonly HEAD_BUILD="head"
readonly SUITE_HARNESS="harness"

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
local_state_dir="${local_root}/.testing/bench"
local_provider_socket="${local_state_dir}/provider/${PROVIDER_SOCKET_NAME}"

# build_name turns a Git ref into a directory name. The binaries of each ref
# live in their own directory under bin/, so two harness builds can be
# compared without rebuilding between jobs.
build_name() {
	local ref="$1"

	if [[ -z "${ref}" ]]; then
		printf '%s' "${HEAD_BUILD}"
		return
	fi

	printf '%s' "${ref//\//_}"
}

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

# lock_requirements compiles requirements.in with hashes. It keeps every pin
# requirements.txt already holds unless the input needs a newer one, and skips
# releases younger than MIN_RELEASE_AGE_DAYS.
lock_requirements() {
	local cutoff
	cutoff="$(date -u -d "${MIN_RELEASE_AGE_DAYS} days ago" '+%Y-%m-%dT%H:%M:%SZ')"
	docker run --rm \
		--user "$(id -u):$(id -g)" \
		--tmpfs /tmp \
		-e UV_CACHE_DIR=/tmp/uv-cache \
		-v "${source_dir}:/bench" \
		-w /bench \
		"${UV_IMAGE}" uv pip compile requirements.in \
		--override overrides.txt \
		--output-file requirements.txt \
		--generate-hashes \
		--no-header \
		--python-version "${BENCH_PYTHON_VERSION}" \
		--exclude-newer "${cutoff}"
	log INFO "locked ${source_dir}/requirements.txt with releases before ${cutoff}"
}

build_binaries() {
	local ref="${BENCH_REF:-}"
	local name
	local version
	local build_root="${repo_root}"
	local local_build_root

	require_pattern BENCH_REF "${ref}" "${REF_PATTERN}"
	name="$(build_name "${ref}")"
	local bin_dir="${state_dir}/bin/${name}"
	local local_bin_dir="${local_state_dir}/bin/${name}"

	if [[ -n "${ref}" ]]; then
		# Peen at ref is built from an exported tree, so the checkout's
		# working state never leaks into it. The relay and the driver come
		# from this checkout, because an older ref may predate them.
		version="$(git -C "${local_root}" describe --tags --always "${ref}")"
		build_root="${state_dir}/src/${name}"
		local_build_root="${local_state_dir}/src/${name}"
		if [[ ! -d "${local_build_root}" ]]; then
			mkdir -p "${local_build_root}"
			git -C "${local_root}" archive --format=tar "${ref}" |
				tar -x -C "${local_build_root}"
		fi
	else
		version="$(git -C "${local_root}" describe --tags --always --dirty)"
	fi

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
		-e BUILD_ROOT="${build_root}" \
		-v "${repo_root}:${repo_root}" \
		-w "${repo_root}" \
		"${DEV_IMAGE}" \
		sh -ceu '
			(cd "${BUILD_ROOT}" && go build -trimpath \
				-ldflags "-s -w -X main.buildVersion=${BUILD_VERSION}" \
				-o "${BIN_DIR}/peen" ./cmd)
			go build -trimpath -ldflags "-s -w" \
				-o "${BIN_DIR}/relay" ./cmd/benchrelay
			go build -trimpath -ldflags "-s -w" \
				-o "${BIN_DIR}/driver" ./cmd/benchdriver
		'
	printf '%s\n' "${version}" >"${local_bin_dir}/peen.version"
	log INFO "built peen ${version}, relay and driver into ${bin_dir}"
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
	# The proxy leaves its socket behind, and start_proxy refuses a leftover.
	rm -f "${local_provider_socket}"
	log INFO "stopped ${PROXY_CONTAINER}"
}

run_suite() {
	local suite="$1"
	local agent="${BENCH_AGENT:-${AGENT_PEEN}}"
	local job="${BENCH_JOB:-${suite}-${agent}-$(date -u '+%Y%m%d-%H%M%S')}"
	local config
	local socket_group
	local harness_ref="${BENCH_HARNESS_REF:-}"
	local build

	require_pattern BENCH_JOB "${job}" "${NAME_PATTERN}"
	require_pattern BENCH_AGENT "${agent}" "${NAME_PATTERN}"
	require_pattern BENCH_TASKS "${BENCH_TASKS:-}" "${TASKS_PATTERN}"
	require_pattern BENCH_MODEL "${BENCH_MODEL:-}" "${MODEL_PATTERN}"
	require_pattern BENCH_COMPACTION_MODEL "${BENCH_COMPACTION_MODEL:-}" "${MODEL_PATTERN}"
	require_pattern BENCH_CONTEXT_TOKENS "${BENCH_CONTEXT_TOKENS:-}" "${NUMBER_PATTERN}"
	require_pattern BENCH_CONCURRENCY "${BENCH_CONCURRENCY:-}" "${NUMBER_PATTERN}"
	require_pattern BENCH_ATTEMPTS "${BENCH_ATTEMPTS:-}" "${NUMBER_PATTERN}"
	require_pattern BENCH_TIER "${BENCH_TIER:-}" "${TIER_PATTERN}"
	require_pattern BENCH_HARNESS_REF "${harness_ref}" "${REF_PATTERN}"
	require_image

	build="$(build_name "${harness_ref}")"
	local bin_dir="${state_dir}/bin/${build}"
	local local_bin_dir="${local_state_dir}/bin/${build}"

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
		-e BENCH_ATTEMPTS \
		-e BENCH_TIER \
		-e BENCH_PEEN_SETTINGS \
		"${DRIVER_IMAGE}" python "${source_dir}/job.py")"

	socket_group="$(stat -c '%g' "${DOCKER_SOCKET}")"
	log INFO "running job ${job} from ${config} with build ${build}"
	driver \
		--name "${RUN_CONTAINER_PREFIX}${job}" \
		--group-add "${socket_group}" \
		-e PYTHONPATH="${source_dir}" \
		-e BENCH_BIN_DIR="${bin_dir}" \
		-e BENCH_STATE_DIR="${state_dir}" \
		-v "${DOCKER_SOCKET}:${DOCKER_SOCKET}" \
		"${DRIVER_IMAGE}" harbor run --config "${config}"
	log INFO "job ${job} finished, trials are under ${state_dir}/jobs/${job}"

	if [[ "${suite}" == "${SUITE_HARNESS}" ]]; then
		harness_tool audit "${job}"
		harness_tool report "${job}"
	fi
}

# harness_tool runs one of the harness audit commands in the driver image.
# They read the jobs and the proxy log and write only inside the job.
harness_tool() {
	local tool="$1"
	shift
	local name

	for name in "$@"; do
		require_pattern JOB "${name}" "${NAME_PATTERN}"
	done

	require_image
	driver "${DRIVER_IMAGE}" \
		python -I "${source_dir}/harness/${tool}.py" \
		--state-dir "${state_dir}" "$@"
}

# run_selftest builds its test databases from Peen's own migrations, mounted
# read-only, so a schema change that breaks an audit query fails here.
run_selftest() {
	local migrations="${repo_root}/internal/pkg/db/migrations/sqlite"

	require_image
	driver \
		-v "${migrations}:${migrations}:ro" \
		-e BENCH_MIGRATIONS_DIR="${migrations}" \
		-e PYTHONDONTWRITEBYTECODE=1 \
		"${DRIVER_IMAGE}" \
		python -m pytest -q -p no:cacheprovider "${source_dir}/harness/tests"
}

command="${1:-}"
case "${command}" in
image) build_image ;;
lock) lock_requirements ;;
build) build_binaries ;;
datasets) download_datasets ;;
proxy) start_proxy ;;
proxy-stop) stop_proxy ;;
run)
	case "${2:-}" in
	aider | lhtb | eval | evolve | harness) run_suite "$2" ;;
	*)
		log ERROR "run needs a suite: aider, lhtb, eval, evolve, or harness"
		exit 1
		;;
	esac
	;;
audit | report)
	if [[ -z "${2:-}" ]]; then
		log ERROR "${command} needs a job name, set BENCH_JOB"
		exit 1
	fi
	harness_tool "${command}" "$2"
	;;
compare)
	if [[ -z "${2:-}" || -z "${3:-}" ]]; then
		log ERROR "compare needs two job names, set BENCH_JOB_A and BENCH_JOB_B"
		exit 1
	fi
	harness_tool compare "$2" "$3"
	;;
selftest) run_selftest ;;
*)
	log ERROR "unknown bench command: ${command}"
	exit 1
	;;
esac
