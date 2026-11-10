#!/bin/bash
# Grade the version, and after a Peen run that mcp.status reported the broken server as failed, the turn still completed, and no tool of that server was called.
set -uo pipefail

python3 /tests/grade.py
