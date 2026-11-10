#!/bin/bash
# Grade the report, and after a Peen run that every refused change was followed by a fresh readFile and not a blind retry.
set -uo pipefail

python3 /tests/grade.py
