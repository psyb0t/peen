#!/bin/bash
# Grade the ledger: the line was appended exactly once after the kill. After a Peen run,
# the worker was killed during the command, the agent inspected the retained work,
# recorded a resolveExecutionActivity decision with evidence, resumed the work, left
# no step uncertain, and did not append by hand.
set -uo pipefail

python3 /tests/grade.py
