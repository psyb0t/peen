#!/bin/bash
# Grade twenty background jobs: every job file is written and the count is
# twenty. After a Peen run, twenty jobs are tracked in SQLite, all exited with
# code 0 and none still running, the agent started them in the background, and
# it called listJobs.
set -uo pipefail

python3 /tests/grade.py
