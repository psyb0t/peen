#!/bin/bash
# Grade ten sessions at once: each tree holds its own output and nothing else
# changed. After a Peen run, all ten turns completed and no session's tool
# calls touched another session's tree.
set -uo pipefail

python3 /tests/grade.py
