#!/bin/bash
# Grade the status file. After a Peen run, the dropped connection was retried as a
# transport failure, or as the bad gateway the benchmark relay turns it into, and
# the turn completed.
set -uo pipefail

python3 /tests/grade.py
