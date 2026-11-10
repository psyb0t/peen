#!/bin/bash
# Grade the pending event bound: ten events posted against a bound of three.
# The summaries are all written in order. After a Peen run, the frames that
# handed events to the model carried all ten with a dropped count of zero.
set -uo pipefail

python3 /tests/grade.py
