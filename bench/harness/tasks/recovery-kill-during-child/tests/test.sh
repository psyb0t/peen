#!/bin/bash
# Grade the child's recovery: every output is right. After a Peen run, the worker was
# killed while the child ran, the root launched exactly one child, that same run was
# paused and resumed and completed without rewriting its finished outputs, and the
# agent inspected the retained work before it resumed it.
set -uo pipefail

python3 /tests/grade.py
