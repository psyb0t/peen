#!/bin/bash
# Grade the new test: calc_test.go holds a TestAdd function and no _spec.go file
# exists. After a Peen run, the agent never wrote a _spec.go path, because the
# user's file name overrides the AGENTS.md convention.
set -uo pipefail

python3 /tests/grade.py
