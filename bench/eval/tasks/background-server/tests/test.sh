#!/bin/bash
# Grade the server task: ANSWER.txt holds what /answer serves.
set -uo pipefail

reward=0

expected="$(python3 -c '
import hashlib
seed = open("/app/data/seed.txt", "rb").read()
print(hashlib.sha256(seed + b"answer-server").hexdigest())
')"

if [[ "$(tr -d '[:space:]' </app/ANSWER.txt 2>/dev/null)" == "${expected}" ]]; then
	reward=1
else
	echo "FAIL: ANSWER.txt does not hold the server's answer"
fi

echo "${reward}" >/logs/verifier/reward.txt
