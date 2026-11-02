#!/bin/bash
# Grade the delegation task: both packages pass their hidden tests, and the
# agent launched at least two child agents. The oracle has no frame stream,
# so only the tests count for it.
set -uo pipefail

reward=1
cd /app || exit 1

fail() {
	echo "FAIL: $*"
	reward=0
}

cp /tests/alpha_hidden_test.go alpha/
cp /tests/beta_hidden_test.go beta/
if ! go test ./...; then
	fail "the hidden tests fail"
fi

if [[ -f /peen/frames.jsonl ]]; then
	launches="$(python3 /tests/frames.py /peen/frames.jsonl)"
	if ((launches < 2)); then
		fail "the agent launched ${launches} child agents, not 2"
	fi
fi

echo "${reward}" >/logs/verifier/reward.txt
