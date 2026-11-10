#!/bin/bash
# Grade a learned rule carried to a second session: the first report is fixed,
# a rule file holds the correction, and the second report starts with the
# header. After a Peen run, the first session saved the rule with its tools in
# the workspace learned directory, the second session never read the first
# report, and nothing was written to the global learned directory.
set -uo pipefail

python3 /tests/grade.py
