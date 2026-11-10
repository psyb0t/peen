#!/bin/bash
# Grade the child round limit (PEEN_MAX_CHILD_AGENT_TURNS=1): the result file is
# right. After a Peen run, the child run failed, the failure reached the root as
# an agent.failed event, and the root wrote the result itself.
set -uo pipefail

python3 /tests/grade.py
