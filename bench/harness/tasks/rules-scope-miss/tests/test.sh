#!/bin/bash
# Grade the edit: guide.md changed in the one way asked and nothing else. After
# a Peen run, the Python-scoped rule never reached the model, because no
# Python file was touched.
set -uo pipefail

python3 /tests/grade.py
