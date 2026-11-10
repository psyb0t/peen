#!/bin/bash
# Grade the tool round limit: with four rounds allowed, a seven-round task ends
# its first turn as failed for rounds and the next turn finishes it. The
# visited list is right, and the first turn made no more than the rounds it was
# allowed.
set -uo pipefail

python3 /tests/grade.py
