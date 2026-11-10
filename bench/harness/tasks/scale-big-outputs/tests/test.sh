#!/bin/bash
# Grade huge outputs: the alert code and the last word are right and the data
# files are whole. After a Peen run, the turn completed and no tool result
# passed the result size cap.
set -uo pipefail

python3 /tests/grade.py
