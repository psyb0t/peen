#!/bin/bash
# Grade the job.exited wake: the handler's file is written. After a Peen run,
# the first turn started the command in the background and ended before the
# job did, and a second turn started by the job.exited event wrote the file.
set -uo pipefail

python3 /tests/grade.py
