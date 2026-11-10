#!/bin/bash
# Grade the queue limit: a burst of eight messages against a queue of two. The
# log holds only valid lines. After a Peen run, the surplus was refused with
# the queue-full code, and every message was either refused, queued, or
# started a turn.
set -uo pipefail

python3 /tests/grade.py
