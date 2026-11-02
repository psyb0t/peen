#!/bin/bash
# Grade the greeting change: the program prints the new greeting, and the
# change went through spec.txt and gen.sh, as the workspace hook requires.
set -uo pipefail

reward=1
cd /app || exit 1

fail() {
	echo "FAIL: $*"
	reward=0
}

if [[ "$(go run . 2>&1)" != "Hello, Peen" ]]; then
	fail "the program does not print Hello, Peen"
fi

if ! grep -qx "greeting=Hello, Peen" spec.txt; then
	fail "spec.txt does not hold the new greeting"
fi

expected="$(mktemp -d)"
trap 'rm -rf "${expected}"' EXIT
cp spec.txt gen.sh "${expected}/"
sh "${expected}/gen.sh"
if ! cmp -s "${expected}/generated/greeting.go" generated/greeting.go; then
	fail "generated/greeting.go is not what gen.sh writes from spec.txt"
fi

echo "${reward}" >/logs/verifier/reward.txt
