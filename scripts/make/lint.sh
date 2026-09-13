#!/bin/bash

set -euo pipefail

log_file="${LOG_FILE:-/tmp/peen-lint.log}"

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
		'{time:$time,level:$level,file:$file,line:$line,func:$func,msg:$msg}'
}

on_error() {
	local exit_code=$?
	log ERROR "lint runner failed exit=${exit_code}"
	exit "${exit_code}"
}

trap on_error ERR
exec > >(tee -a "${log_file}") 2>&1

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

check_embedded_assets() {
	local embedded_assets
	local embedded_asset_path
	local relative_asset_path
	local repository_root

	if ! repository_root="$(git rev-parse --show-toplevel)"; then
		log ERROR "locate repository root"
		return 1
	fi
	if ! embedded_assets="$(go list -json ./... | jq -r '
		select(.EmbedFiles != null) |
		.Dir as $directory |
		.EmbedFiles[] |
		($directory + "/" + .)
	')"; then
		log ERROR "discover Go embedded assets"
		return 1
	fi
	if [[ -z "${embedded_assets}" ]]; then
		log WARN "no Go embedded assets found"
		return 0
	fi

	while IFS= read -r embedded_asset_path; do
		relative_asset_path="${embedded_asset_path#"${repository_root}/"}"
		if [[ "${relative_asset_path}" == "${embedded_asset_path}" ]]; then
			log ERROR "embedded asset outside repository path=${embedded_asset_path}"
			return 1
		fi
		if git check-ignore -q -- "${relative_asset_path}"; then
			log ERROR "embedded asset is ignored path=${relative_asset_path}"
			return 1
		fi
	done <<<"${embedded_assets}"
}

if [[ -n "${DEBUG:-}" ]]; then
	log DEBUG "starting Peen lint checks"
fi
log INFO "checking Go embedded assets"
if ! check_embedded_assets; then
	exit 1
fi
log INFO "Go embedded assets are visible to Git"

if ! bash "${script_dir}/servicepack/lint.sh"; then
	log ERROR "run Servicepack lint checks"
	exit 1
fi
log INFO "Peen lint checks completed"
