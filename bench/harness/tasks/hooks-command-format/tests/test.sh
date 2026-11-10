#!/bin/bash
# Grade the new Go file: it starts with the license header that only the
# postWriteFile command hook adds, and the code still works. After a Peen run,
# the hook's own log shows it ran on that path.
set -uo pipefail

python3 /tests/grade.py
