#!/bin/bash
# Count the files in the tree.
set -euo pipefail

find /app/project -type f | wc -l >/app/out/count.txt
