#!/bin/bash
# Grade the wake cap (PEEN_MAX_EVENT_WAKES_PER_HOUR=3): all five tick files
# exist. After a Peen run, exactly three turns were started by tick events, the
# last two events started none, and they reached the model with the user's
# later message.
set -uo pipefail

python3 /tests/grade.py
