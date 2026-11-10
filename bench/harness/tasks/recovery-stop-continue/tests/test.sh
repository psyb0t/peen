#!/bin/bash
# Grade the stop: every output is right at the end. After a Peen run, the stop cancelled
# the turn mid-work, an unrelated message did not move the stopped work forward, and
# only the continue message led the agent to inspect and resume it.
set -uo pipefail

python3 /tests/grade.py
