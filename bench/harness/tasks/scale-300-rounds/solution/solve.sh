#!/bin/bash
# Write each input in capitals.
set -euo pipefail

for item in /app/in/*.txt; do
	tr '[:lower:]' '[:upper:]' <"${item}" >"/app/out/$(basename "${item}")"
done
