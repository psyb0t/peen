#!/bin/bash
# Write both files.
set -euo pipefail

printf 'ready\n' >/app/start.txt
printf 'handled\n' >/app/ci-report.txt
