#!/bin/bash
# Grade the notes file: the workspace rule overrides the global one, so the
# first line is STATUS: REVIEWED. After a Peen run, both rules were in the
# system prompt, the global one first, because rules add and never replace.
set -uo pipefail

python3 /tests/grade.py
