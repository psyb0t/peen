#!/bin/bash
# Grade the match list: it holds exactly the sorted paths of the corpus files
# that contain the token, computed from the corpus itself. After a Peen run, the
# agent used searchText and ran no grep, rg, or find through runCommand.
set -uo pipefail

python3 /tests/grade.py
