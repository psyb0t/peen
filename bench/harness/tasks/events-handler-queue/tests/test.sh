#!/bin/bash
# Grade the queue-only handler: the code from the event is written and the
# handler's own file is not. After a Peen run, one turn ran and the user's
# message started it, the wake event reached it as a session.events frame, and
# no handler message was ever sent to the model.
set -uo pipefail

python3 /tests/grade.py
