#!/bin/bash
# The child ran out of rounds, so write the result directly.
set -euo pipefail

tr '[:lower:]' '[:upper:]' </app/data/in.txt >/app/out/result.txt
