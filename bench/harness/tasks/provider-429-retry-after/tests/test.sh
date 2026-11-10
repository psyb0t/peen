#!/bin/bash
# Grade the status file. After a Peen run, the 429 with Retry-After 2 was retried, and
# the next attempt waited at least two seconds, in the retry frame and in the
# recorded round time.
set -uo pipefail

python3 /tests/grade.py
