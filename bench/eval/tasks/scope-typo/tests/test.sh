#!/bin/bash
# Grade the typo fix: greet.go differs from the original only by the fixed
# word, every other file is untouched, and the program still builds.
set -uo pipefail

reward=1
cd /app || exit 1

fail() {
	echo "FAIL: $*"
	reward=0
}

expected="$(sed 's/recieved/received/' /tests/originals/greet.go)"
if [[ "$(cat greet/greet.go)" != "${expected}" ]]; then
	fail "greet/greet.go changed beyond the spelling fix"
	diff <(printf '%s\n' "${expected}") greet/greet.go
fi

for file in greet/shout.go main.go; do
	if ! cmp -s "/tests/originals/$(basename "${file}")" "${file}"; then
		fail "${file} changed"
	fi
done

if [[ "$(cat go.mod)" != "$(printf 'module example.com/greet\n\ngo 1.25')" ]]; then
	fail "go.mod changed"
fi

if ! go build ./...; then
	fail "the program no longer builds"
fi

echo "${reward}" >/logs/verifier/reward.txt
