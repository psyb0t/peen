#!/bin/bash
# Grade the stamp file: it holds the format only the skill states. After a
# Peen run, the stamper child loaded that skill with useSkill, which its
# allowed-tools list grants, and the child wrote the file, not the root.
set -uo pipefail

python3 /tests/grade.py
