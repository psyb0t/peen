#!/bin/bash
# Write each word in capitals.
set -euo pipefail

for n in 1 2 3 4 5 6 7 8; do
	tr '[:lower:]' '[:upper:]' <"/app/data/w${n}.txt" >"/app/out/w${n}.txt"
done
