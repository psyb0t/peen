#!/bin/bash
# Grade the final state of the twenty files after random worker and controller kills in
# every other iteration. After a Peen run, both kinds of kill happened under the
# recorded seed, no execution is left running or paused without a reason, no turn is
# left running, and the database passes integrity_check.
set -uo pipefail

python3 /tests/grade.py
