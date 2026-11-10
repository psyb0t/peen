#!/bin/bash
# Grade the price file: it holds the cents form that only the learned rule
# states. After a Peen run, the learned rule was in the system prompt.
set -uo pipefail

python3 /tests/grade.py
