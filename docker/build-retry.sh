#!/bin/sh
# build-retry runs a build step that reaches the network, such as a package
# install, and retries it with exponential backoff. One DNS or mirror blip on
# a CI runner used to fail the whole image build.
#
# Usage: build-retry.sh COMMAND [ARGUMENT...]
#
# BUILD_RETRY_ATTEMPTS (default 5) bounds the attempts, including the first.
# BUILD_RETRY_BACKOFF_SECONDS (default 15) is the wait before the second
# attempt, doubled after each failure. The script exits with the last
# attempt's status. It is POSIX sh because it runs in Alpine and Ubuntu build
# stages alike, and it writes JSON log lines to stderr.
set -eu

readonly DEFAULT_ATTEMPTS=5
readonly DEFAULT_BACKOFF_SECONDS=15

log() {
	level="$1"
	shift

	printf '{"time":"%s","level":"%s","file":"build-retry.sh","msg":"%s"}\n' \
		"$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "${level}" "$*" >&2
}

main() {
	if [ "$#" -eq 0 ]; then
		log ERROR "usage: build-retry.sh COMMAND [ARGUMENT...]"
		exit 2
	fi

	attempts="${BUILD_RETRY_ATTEMPTS:-${DEFAULT_ATTEMPTS}}"
	delay="${BUILD_RETRY_BACKOFF_SECONDS:-${DEFAULT_BACKOFF_SECONDS}}"
	attempt=1

	while :; do
		status=0
		"$@" || status=$?

		if [ "${status}" -eq 0 ]; then
			return 0
		fi

		if [ "${attempt}" -ge "${attempts}" ]; then
			log ERROR "build step failed after ${attempt} attempt(s) exit=${status} command=$1"
			return "${status}"
		fi

		log WARN "build step failed, retrying attempt=${attempt} exit=${status} delay=${delay}s command=$1"
		sleep "${delay}"
		delay=$((delay * 2))
		attempt=$((attempt + 1))
	done
}

main "$@"
