#!/bin/bash
# Write each item's word in capitals.
set -euo pipefail

for item in /app/items/*.txt; do
	tr '[:lower:]' '[:upper:]' <"${item}" >"/app/out/$(basename "${item}")"
done
