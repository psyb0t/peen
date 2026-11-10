#!/bin/bash
# Write both files.
set -euo pipefail

printf 'seen\n' >/app/out/tern.txt
printf 'done\n' >/app/out/done.txt
