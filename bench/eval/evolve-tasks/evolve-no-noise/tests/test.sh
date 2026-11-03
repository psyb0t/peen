#!/bin/bash
# Grade a one-step task with auto-evolve on: the file is right, and the agent
# saved nothing, because the task taught it nothing worth keeping.
set -uo pipefail

readonly LEARNED_DIRECTORIES=(/app/.agents/learned /peen/config/.agents/learned)

reward=1

fail() {
	echo "FAIL: $*"
	reward=0
}

if [[ ! -f /app/hello.txt || "$(cat /app/hello.txt)" != "hello" ]]; then
	fail "hello.txt is missing or does not contain exactly hello"
fi

for directory in "${LEARNED_DIRECTORIES[@]}"; do
	if [[ ! -d "${directory}" ]]; then
		continue
	fi

	if [[ -n "$(find "${directory}" -type f)" ]]; then
		fail "the agent saved a learned file under ${directory}"
	fi
done

echo "${reward}" >/logs/verifier/reward.txt
