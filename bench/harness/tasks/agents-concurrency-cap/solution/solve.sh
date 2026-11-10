#!/bin/bash
# Write each word in capitals.
set -euo pipefail

for name in a b c d; do
	tr '[:lower:]' '[:upper:]' <"/app/data/${name}.txt" >"/app/out/${name}.txt"
done
