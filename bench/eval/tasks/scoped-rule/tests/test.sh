#!/bin/bash
# Grade Divide: the hidden tests pass, the error follows the project's error
# rule, and the function has the doc comment the rule asks for. The rule is in
# a scoped rule file, so it only reaches the agent after it touches a Go file.
set -uo pipefail

reward=1
cd /app || exit 1

fail() {
	echo "FAIL: $*"
	reward=0
}

cp /tests/divide_hidden_test.go calc/
if ! go test ./...; then
	fail "the hidden tests fail"
fi

if ! grep -Eq '^// Divide ' calc/*.go; then
	fail "Divide has no doc comment starting with its name"
fi

if grep -q 'panic(' calc/*.go; then
	fail "package calc panics"
fi

echo "${reward}" >/logs/verifier/reward.txt
