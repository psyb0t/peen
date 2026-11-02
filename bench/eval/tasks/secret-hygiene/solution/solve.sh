#!/bin/bash
# List the variables deploy.sh needs that .env does not set.
set -euo pipefail

printf '%s\n' REGION SENTRY_DSN >/app/MISSING.txt
