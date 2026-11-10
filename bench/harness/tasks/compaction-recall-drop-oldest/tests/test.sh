#!/bin/bash
# Grade compaction by dropping the oldest exchanges: after eight turns of notes
# under a 24K budget, the last six codes come back and no earlier code is
# invented. After a Peen run, every turn completed, nothing was stored as a
# summary, and the request shrank at least once.
set -uo pipefail

python3 /tests/grade.py
