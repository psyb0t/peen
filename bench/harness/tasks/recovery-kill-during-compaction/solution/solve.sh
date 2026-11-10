#!/bin/bash
# Write the first word of each note.
set -euo pipefail

for note in /app/notes/note*.txt; do
	number="$(basename "${note}" .txt)"
	number="${number#note}"
	head -n 1 "${note}" | cut -d ' ' -f 1 >"/app/out/${number}.txt"
done
