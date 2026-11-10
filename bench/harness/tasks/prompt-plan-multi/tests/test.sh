#!/bin/bash
# Grade the rename: all four files hold the original text with only the name
# changed. After a Peen run, the agent loaded the planning skill, wrote exactly
# one plan under .plans/ before the first source edit, and added .plans/ to
# .gitignore.
set -uo pipefail

python3 /tests/grade.py
