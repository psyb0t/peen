#!/bin/bash
# Grade the correction task with auto-evolve on: the code follows the
# correction, the agent saved the correction as a learned rule, and it left the
# user's own rule file alone and wrote no other harness file.
set -uo pipefail

readonly LEARNED_RULES="/app/.agents/learned/rules"
readonly USER_RULE="/app/.agents/rules/style.md"

reward=1
cd /app || exit 1

fail() {
	echo "FAIL: $*"
	reward=0
}

cp /tests/calc_hidden_test.go calc/
if ! go test ./...; then
	fail "the hidden tests fail"
fi

if ! grep -rqs 'calc: ' "${LEARNED_RULES}"; then
	fail "no learned rule under ${LEARNED_RULES} records the calc: prefix"
fi

if ! cmp -s /tests/style.md "${USER_RULE}"; then
	fail "the user's rule file ${USER_RULE} changed"
fi

if [[ -n "$(find /app/.agents/rules -type f ! -name style.md)" ]]; then
	fail "the agent added a file to .agents/rules instead of .agents/learned"
fi

if [[ -e /app/AGENTS.md ]]; then
	fail "the agent wrote AGENTS.md"
fi

echo "${reward}" >/logs/verifier/reward.txt
