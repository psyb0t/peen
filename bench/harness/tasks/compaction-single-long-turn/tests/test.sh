#!/bin/bash
# Grade compaction inside one turn: twenty-four records are read in a single
# turn under a 24K budget and every serial comes back. After a Peen run, the
# turn completed, a summary request ran during it, and no oldest history was
# left out without a summary.
set -uo pipefail

python3 /tests/grade.py
