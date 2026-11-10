#!/bin/bash
# Grade the four writes behind slow hook commands: a timed-out pre hook denies
# the write by default and with onFailure deny, and onFailure continue lets it
# through; a timed-out post hook lets the write stand. After a Peen run, the
# post hook's failure was published as a hook.action.failed event.
set -uo pipefail

python3 /tests/grade.py
