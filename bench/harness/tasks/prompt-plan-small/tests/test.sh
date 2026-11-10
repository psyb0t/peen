#!/bin/bash
# Grade the one-line fix: calc.py is the original with only the divisor changed.
# After a Peen run, the agent wrote no plan and loaded no planning skill, since
# a one-line fix gets no plan.
set -uo pipefail

python3 /tests/grade.py
