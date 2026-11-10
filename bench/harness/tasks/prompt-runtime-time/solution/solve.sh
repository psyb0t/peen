#!/bin/bash
# Write the container's UTC date.
set -euo pipefail

date -u +%F >/app/today.txt
