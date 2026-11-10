#!/bin/bash
# Grade the nesting limit (PEEN_MAX_CHILD_AGENT_DEPTH=1): the file holds the
# child's own fallback word. After a Peen run, exactly one child ran, at depth
# 1, it ended completed, and no grandchild was ever launched.
set -uo pipefail

python3 /tests/grade.py
