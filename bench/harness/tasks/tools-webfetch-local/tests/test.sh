#!/bin/bash
# Grade both files, and after a Peen run that webFetch read both URLs, the 404 came back as a status and not a tool error, and curl was not used.
set -uo pipefail

python3 /tests/grade.py
