#!/bin/bash
# Write the file with the required first line.
set -euo pipefail

printf '# owner: platform-team\n\n\ndef health():\n    return "ok"\n' >/app/svc/health.py
