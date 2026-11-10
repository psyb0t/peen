#!/bin/bash
# Grade the crash recovery after the controller was killed and restarted mid-turn: every
# output is right. After a Peen run, the kill happened, the first turn was recorded as
# failed, the agent inspected the retained work before it resumed it, and it did not
# write completed outputs again.
set -uo pipefail

python3 /tests/grade.py
