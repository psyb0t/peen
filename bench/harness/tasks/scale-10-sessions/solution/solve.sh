#!/bin/bash
# Write each tree's output in capitals.
set -euo pipefail

for n in $(seq 0 9); do
	tr '[:lower:]' '[:upper:]' <"/app/w${n}/input.txt" >"/app/w${n}/out.txt"
done
