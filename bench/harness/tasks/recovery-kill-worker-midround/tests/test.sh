#!/bin/bash
# Grade the crash recovery: every output is right after the worker was killed
# mid-turn and the agent was asked to continue. After a Peen run, the kill
# happened, the first turn was recorded as failed, and the agent inspected the
# retained work before it resumed it.
set -uo pipefail

python3 /tests/grade.py
