#!/bin/bash
# Grade the fan-out (PEEN_MAX_CONCURRENT_AGENT_RUNS=3): all eight files are
# right. After a Peen run, eight children completed, never more than three ran
# at once, each output file was written once, and the root wrote none.
set -uo pipefail

python3 /tests/grade.py
