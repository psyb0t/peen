#!/bin/bash
# Write the file and save nothing.
set -euo pipefail

printf '%s\n' hello >/app/hello.txt
