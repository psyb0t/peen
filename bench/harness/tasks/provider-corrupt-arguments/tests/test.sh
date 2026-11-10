#!/bin/bash
# Grade the status file. After a Peen run, the corrupted tool call came back to the
# model as a tool error, a later call succeeded, and the turn completed.
set -uo pipefail

python3 /tests/grade.py
