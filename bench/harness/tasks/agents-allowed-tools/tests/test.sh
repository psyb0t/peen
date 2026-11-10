#!/bin/bash
# Grade the restricted child: the word file is right. After a Peen run, the stored
# note-writer agent ran and finished, never used runCommand or any tool its
# allowed-tools leaves out, and wrote the file with the file tools itself.
set -uo pipefail

python3 /tests/grade.py
