#!/bin/bash
# Make the two moves.
set -euo pipefail

cd /app
mv b/report.txt b/report.old.txt
mv a/report.txt b/report.txt
