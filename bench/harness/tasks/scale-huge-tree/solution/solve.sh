#!/bin/bash
# Find the marker line and write its value.
set -euo pipefail

grep -rh 'ORCHID-LEDGER-7' /app/tree | sed 's/.*value=//' >/app/answer.txt
