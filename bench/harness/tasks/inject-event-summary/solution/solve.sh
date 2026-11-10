#!/bin/bash
# Write both files.
set -euo pipefail

printf 'red\n' >/app/first.txt
printf 'blue\n' >/app/second.txt
