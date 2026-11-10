#!/bin/bash
# Grade the outputs after the client socket dropped mid-turn and reattached from the last
# sequence. After a Peen run, the two phases joined repeat no sequence and lose none
# against the stored feed, the reattached feed delivered the turn's end, and the
# turn still completed.
set -uo pipefail

python3 /tests/grade.py
