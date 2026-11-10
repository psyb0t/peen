#!/bin/bash
# Grade that nothing was written, because every provider request answers 401. After a
# Peen run, Peen made one attempt and no retry, recorded the model call as failed
# with a classification, and ended the turn with turn.failed and an error.
set -uo pipefail

python3 /tests/grade.py
