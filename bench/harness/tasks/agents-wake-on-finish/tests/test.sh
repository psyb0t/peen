#!/bin/bash
# Grade the wake on a child's finish: the result file holds the child's word.
# After a Peen run, the first turn ended while the child still ran, the child's
# finish started a second turn caused by an agent.finished event, and that
# turn wrote the result.
set -uo pipefail

python3 /tests/grade.py
