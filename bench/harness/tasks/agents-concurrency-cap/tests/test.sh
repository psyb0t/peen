#!/bin/bash
# Grade the concurrent run cap (PEEN_MAX_CONCURRENT_AGENT_RUNS=2): all four
# files are right. After a Peen run, a launch was refused for the cap, four
# children completed, and never more than two ran at the same time.
set -uo pipefail

python3 /tests/grade.py
