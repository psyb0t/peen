#!/bin/bash
# Grade ParseSize against the hidden tests.
set -uo pipefail

reward=0
cd /app || exit 1

cp /tests/size_hidden_test.go size/
if go vet ./... && go test ./...; then
	reward=1
fi

echo "${reward}" >/logs/verifier/reward.txt
