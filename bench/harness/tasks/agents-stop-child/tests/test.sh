#!/bin/bash
# Grade stopAgent: the status file is written and no sleep 613 process is left
# behind. After a Peen run, the root called stopAgent on the child's run, the
# run is recorded as cancelled, and the child had started the long command.
set -uo pipefail

python3 /tests/grade.py
