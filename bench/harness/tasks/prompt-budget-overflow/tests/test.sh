#!/bin/bash
# Grade the file: done.txt holds ok. After a Peen run, the oversized AGENTS.md in
# big was refused at the directory change (a failed changeDirectory, not a failed
# turn), and the turn still completed.
set -uo pipefail

python3 /tests/grade.py
