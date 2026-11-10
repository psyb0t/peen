#!/bin/bash
# Grade the file: it starts with the ticket line that only the preUserMessage
# hook states. After a Peen run, the hook's opening checkpoint completed and
# its text was in the turn's system prompt.
set -uo pipefail

python3 /tests/grade.py
