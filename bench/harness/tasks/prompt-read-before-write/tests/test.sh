#!/bin/bash
# Grade the setting change: the file holds the new value and nothing else
# moved. After a Peen run, the agent also read the file before changing it,
# so no change tool was refused for a missing read.
set -uo pipefail

python3 /tests/grade.py
