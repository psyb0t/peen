#!/bin/bash
# Grade the version file: it holds the registry's newest version, not the
# stale go.mod one. After a Peen run, the agent also loaded the core freshness
# skill, whose description covers checking a dependency version.
set -uo pipefail

python3 /tests/grade.py
