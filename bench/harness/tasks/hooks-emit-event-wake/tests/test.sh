#!/bin/bash
# Grade the wake: report.txt comes from the one message the driver sent, and
# processed.txt can only come from the turn the hook's event woke. After a
# Peen run, the event is stored with source hooks, a handler message started
# a second turn, and the driver sent only one message.
set -uo pipefail

python3 /tests/grade.py
