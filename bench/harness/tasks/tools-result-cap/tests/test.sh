#!/bin/bash
# Grade the sku, and after a Peen run that no tool call was repeated and the agent stayed within a handful of calls despite the small tool result cap.
set -uo pipefail

python3 /tests/grade.py
