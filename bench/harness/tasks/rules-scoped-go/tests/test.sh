#!/bin/bash
# Grade the new function: it reverses runes and its doc comment ends with the
# stability line that only the Go-scoped rule states. After a Peen run, the
# scoped rule also reached the model as a rule block after a tool result.
set -uo pipefail

python3 /tests/grade.py
