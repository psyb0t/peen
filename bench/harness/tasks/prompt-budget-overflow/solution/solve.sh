#!/bin/bash
# Write the file.
set -euo pipefail

mkdir -p /app/big && printf 'ok\n' >/app/big/done.txt
