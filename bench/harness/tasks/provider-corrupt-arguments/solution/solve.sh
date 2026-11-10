#!/bin/bash
# Write the status word.
set -euo pipefail

printf 'ready\n' >/app/status.txt
