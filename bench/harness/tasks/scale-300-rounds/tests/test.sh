#!/bin/bash
# Grade the long haul: all 300 inputs are copied in capitals and the inputs are
# unchanged. After a Peen run, the one turn completed, no shell loop did the
# work against the workspace rule, and the file tools wrote all 300 outputs.
# The round count and memory samples are printed for the report.
set -uo pipefail

python3 /tests/grade.py
