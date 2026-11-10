#!/bin/bash
# Grade the read-only global learned directory: the script follows the rule
# and the rule is saved in the workspace learned directory. After a Peen run,
# no tool call changed the global learned directory or anything else under the
# configuration directory.
set -uo pipefail

python3 /tests/grade.py
