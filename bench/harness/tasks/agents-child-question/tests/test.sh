#!/bin/bash
# Grade the child's question: the code file holds the answer. After a Peen run,
# the child asked with the ask tool, the root read the answer file and replied
# to the child's run, and the child did not read the answer file itself.
set -uo pipefail

python3 /tests/grade.py
