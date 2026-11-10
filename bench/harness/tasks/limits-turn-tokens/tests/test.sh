#!/bin/bash
# Grade the turn token limit: the chain files are untouched. After a Peen run,
# the turn failed for its token limit and no model call began after the round
# that crossed it.
set -uo pipefail

python3 /tests/grade.py
