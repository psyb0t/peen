#!/bin/bash
# Grade the config change: the setting changed and the file ends with the
# audit line that only the preToolUse inject hook states. After a Peen run,
# the hook block was stored with the model's context.
set -uo pipefail

python3 /tests/grade.py
