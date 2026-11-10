#!/bin/bash
# Grade the date file: it holds a YYYY-MM-DD date within one day of the container
# clock. After a Peen run, a stored prompt snapshot carries the runtime time
# block with a date within one day of the clock, so the date came from Peen.
set -uo pipefail

python3 /tests/grade.py
