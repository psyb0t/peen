#!/bin/bash

set -euo pipefail

log() {
	local level="$1"
	shift
	local timestamp
	timestamp=$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')
	jq -nc \
		--arg time "$timestamp" \
		--arg level "$level" \
		--arg file "${BASH_SOURCE[1]##*/}" \
		--argjson line "${BASH_LINENO[0]}" \
		--arg func "${FUNCNAME[1]:-main}" \
		--arg msg "$*" \
		'{time:$time,level:$level,file:$file,line:$line,func:$func,msg:$msg}' >&2
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR

log DEBUG "running service registration with quiet dependency logging"
export LOG_LEVEL=error
exec bash "$SCRIPT_DIR/servicepack/service_registration.sh"
