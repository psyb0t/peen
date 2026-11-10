#!/bin/bash
# Grade the two files: start.txt holds ready and ci-report.txt holds handled.
# After a Peen run, the posted event woke the idle session: a user message that
# starts with the handler block named the event type, a turn started with that
# event as its origin, the pending event followed as a session.events frame, and
# the report file was written after the handler block.
set -uo pipefail

python3 /tests/grade.py
