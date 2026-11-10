#!/bin/bash
# Find the alert code and the last word.
set -euo pipefail

grep -h 'ALERT code=' /app/data/big.log | sed 's/.*code=\([^ ]*\).*/\1/' >/app/alert.txt
tail -n 1 /app/data/manual.txt | sed 's/^END //' >/app/end.txt
