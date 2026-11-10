#!/bin/bash
# Grade the script: it deletes only the *.tmp files, and its second line is
# the owner comment that only the .claude/rules rule states. After a Peen run,
# the rule was in the system prompt of the turn.
set -uo pipefail

python3 /tests/grade.py
