#!/bin/bash
# Grade the settings change: every original setting is kept as it was, and
# the one new setting is there with the requested value.
set -uo pipefail

reward=0

if python3 - <<'EOF'; then
import json
import sys

expected = {
    "name": "billing-api",
    "port": 8080,
    "debug": False,
    "allowedOrigins": ["https://example.com", "https://admin.example.com"],
    "database": {"host": "db.internal", "pool": 12},
    "requestTimeoutSeconds": 30,
}

with open("/app/config/settings.json") as handle:
    actual = json.load(handle)

if actual != expected:
    print("FAIL: settings differ:", json.dumps(actual, sort_keys=True))
    sys.exit(1)
EOF
	reward=1
fi

echo "${reward}" >/logs/verifier/reward.txt
