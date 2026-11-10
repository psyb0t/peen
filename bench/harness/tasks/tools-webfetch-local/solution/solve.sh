#!/bin/bash
# Write the first line and the missing status.
set -euo pipefail

echo 'alpha line' >/app/out/first.txt
echo 404 >/app/out/missing.txt
