#!/bin/bash
# Grade the report: summary.md holds done and no other summary file exists. After
# a Peen run, the agent changed into reports before it wrote the file, since only
# the closest AGENTS.md names the extension.
set -uo pipefail

python3 /tests/grade.py
