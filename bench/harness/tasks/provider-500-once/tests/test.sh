#!/bin/bash
# Grade the status file. After a Peen run, the first provider request failed with a 500
# and Peen retried it once: one provider.retry frame, the retry gap inside the
# backoff window, the round recorded one retry and its wasted tokens, and the turn completed.
set -uo pipefail

python3 /tests/grade.py
