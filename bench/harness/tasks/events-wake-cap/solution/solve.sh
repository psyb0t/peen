#!/bin/bash
# Write the five tick files.
set -euo pipefail

for n in 1 2 3 4 5; do
	printf '%s\n' "${n}" >"/app/out/tick-${n}.txt"
done
