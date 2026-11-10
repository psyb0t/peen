#!/bin/bash
# Grade the new module: health.py starts with the convention line of the svc
# AGENTS.md and health() returns ok. After a Peen run, changeDirectory was called
# for svc and every call of it was alone in its tool round, and the svc
# AGENTS.md appears in a stored prompt snapshot.
set -uo pipefail

python3 /tests/grade.py
