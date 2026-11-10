#!/bin/bash
# Grade the status file. After a Peen run, the stalled request was abandoned at the
# 20 s request timeout, long before the 120 s stall ended, and retried, and the turn
# completed.
set -uo pipefail

python3 /tests/grade.py
