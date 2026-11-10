#!/bin/bash
# Grade the queued event: the file the event named and the done file exist. After
# a Peen run, the event was handed to the model in a session.events frame while
# the one turn still ran, and it started no second turn.
set -uo pipefail

python3 /tests/grade.py
