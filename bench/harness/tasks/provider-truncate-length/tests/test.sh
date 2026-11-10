#!/bin/bash
# Grade the status file. After a Peen run, the second model reply was cut off with
# finish_reason length and the turn recorded it: a length round exists, and the
# turn that ended on a length round says finishReason length in turn.completed. A
# later round after the cut is also accepted.
set -uo pipefail

python3 /tests/grade.py
