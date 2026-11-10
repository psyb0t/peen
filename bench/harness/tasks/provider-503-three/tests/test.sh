#!/bin/bash
# Grade the status file. After a Peen run, three 503 answers were retried three times
# with growing delays, the fourth attempt succeeded inside one round, and the turn
# completed.
set -uo pipefail

python3 /tests/grade.py
