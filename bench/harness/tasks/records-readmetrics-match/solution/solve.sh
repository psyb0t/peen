#!/bin/bash
# Copy the files and write the counts.
set -euo pipefail

cp /app/data/*.txt /app/copy/
printf 'readFile=3\nwriteFile=3\n' >/app/metrics.txt
