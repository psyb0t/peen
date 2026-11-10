#!/bin/bash
# Grade a 50,000 file tree: the answer is right and the tree is whole. After a
# Peen run, searchText did the search and no listing or search result passed
# the result size cap.
set -uo pipefail

python3 /tests/grade.py
