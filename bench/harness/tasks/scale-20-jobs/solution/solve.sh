#!/bin/bash
# Write the twenty job files and the count.
set -euo pipefail

for n in $(seq 1 20); do
	echo "done ${n}" >"/app/jobs/${n}.txt"
done
echo 20 >/app/count.txt
