#!/bin/bash
# Count the lines.
set -euo pipefail

wc -l </app/data/log.txt | tr -d ' ' >/app/out/count.txt
