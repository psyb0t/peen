#!/bin/bash
# Grade the status file. After a Peen run, the stream cut mid-body was retried, the
# turn completed, and no assistant message from the cut attempt was stored: no
# incomplete assistant message, and one assistant message per completed round.
set -uo pipefail

python3 /tests/grade.py
