#!/bin/bash
# Grade the message size limit: only the small request was done. After a Peen
# run, the oversized message was refused with a code, no turn and no model call
# came from it, and the small request ran as the only turn.
set -uo pipefail

python3 /tests/grade.py
