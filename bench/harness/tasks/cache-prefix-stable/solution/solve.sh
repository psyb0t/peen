#!/bin/bash
# Write the three words the three turns ask for.
set -euo pipefail

mkdir -p /app/out
printf 'amber\n' >/app/out/1.txt
printf 'cobalt\n' >/app/out/2.txt
printf 'saffron\n' >/app/out/3.txt
