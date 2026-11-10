#!/bin/bash
# Grade two sends in one session that both name the same skill: both files
# carry the skill's header. After a Peen run, the skill was delivered in full
# once and the second send got only the already-loaded note.
set -uo pipefail

python3 /tests/grade.py
