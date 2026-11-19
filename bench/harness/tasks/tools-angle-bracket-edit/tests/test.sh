#!/bin/bash
# Grade the rename: the file holds exactly the renamed class. After a Peen
# run, no stored tool result writes <, >, or & as a JSON escape, so the text
# the model copied into editFile matched the file.
set -uo pipefail

python3 /tests/grade.py
