#!/bin/bash
# Grade four large reads in one session with a small context: every output
# file holds the last line of its source. After a Peen run, summarize
# compaction ran and each compaction fired both the preCompact and the
# postCompact hook, as the hook's own log and the saved checkpoints show.
set -uo pipefail

python3 /tests/grade.py
