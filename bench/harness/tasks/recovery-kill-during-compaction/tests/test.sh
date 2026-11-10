#!/bin/bash
# Grade the notes' first words after a worker kill. After a Peen run, the worker was
# killed while a compaction summary call was in flight, the stored compactions have no
# duplicated range, none is empty, and each has a completed summary call.
set -uo pipefail

python3 /tests/grade.py
