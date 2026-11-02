#!/bin/bash
# Grade the Median fix: the hidden tests and go vet must pass.
set -uo pipefail

reward=0
cd /app || exit 1

cp /tests/median_hidden_test.go stats/
if go vet ./... && go test ./...; then
	reward=1
fi

echo "${reward}" >/logs/verifier/reward.txt
