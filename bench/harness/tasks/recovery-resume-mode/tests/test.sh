#!/bin/bash
# Grade the automatic recovery with PEEN_CRASH_RECOVERY=resume: every output is right with
# no message after the kill. After a Peen run, the controller was killed and started
# again, the driver sent one message, the agent called no resume tool, and every root
# execution completed.
set -uo pipefail

python3 /tests/grade.py
