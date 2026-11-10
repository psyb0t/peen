#!/bin/bash
# Grade the status file. After a Peen run, the 45 s provider outage outlasted the
# retries so the first turn failed after many retries, and the "continue" send
# after the outage ended completed the task.
set -uo pipefail

python3 /tests/grade.py
