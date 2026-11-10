#!/bin/bash
# Grade the rule cap: with 65 rule files the first turn is refused before any
# model call (the documented hard limit is 64), and once the extra rules are
# gone the same request completes and writes the file.
set -uo pipefail

python3 /tests/grade.py
