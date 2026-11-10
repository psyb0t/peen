#!/bin/bash
# Grade that the count equals the tool calls made before the readMetrics call, which the metrics do not yet include.
set -uo pipefail

python3 /tests/grade.py
