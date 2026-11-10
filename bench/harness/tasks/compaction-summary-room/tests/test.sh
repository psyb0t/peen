#!/bin/bash
# Grade a summary room that does not fit at first: with half the context set
# aside for the summary, Peen still compacts by trying a smaller room. All
# twelve codes come back. After a Peen run, every turn completed and a summary
# was stored.
set -uo pipefail

python3 /tests/grade.py
