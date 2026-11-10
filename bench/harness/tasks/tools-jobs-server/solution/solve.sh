#!/bin/bash
# Write the status and noise results.
set -euo pipefail

echo 'service-status: green-3141' >/app/out/status.txt
echo 'DONE code=7391' >/app/out/noise.txt
