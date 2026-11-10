#!/bin/bash
# Grade the event during a Stop: nothing was written, because the turn was
# stopped and the late event did not wake the session. After a Peen run, the
# one turn ended cancelled, the event was recorded, and no turn was ever
# started by an event.
set -uo pipefail

python3 /tests/grade.py
