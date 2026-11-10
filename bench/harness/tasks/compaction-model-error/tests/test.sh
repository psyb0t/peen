#!/bin/bash
# Grade a compaction model that answers one 500: the turn still completes and
# no code is invented. After a Peen run, a compaction request met the injected
# error, and either a summary was stored and every code came back, or the
# documented fallback left the oldest history out.
set -uo pipefail

python3 /tests/grade.py
