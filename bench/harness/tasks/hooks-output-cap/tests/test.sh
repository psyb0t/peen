#!/bin/bash
# Grade three writes behind hook commands under a 1024 byte output cap: a
# short output changes nothing, a pre hook over the cap denies the write, and
# a post hook over the cap lets the write stand. After a Peen run, the post
# hook's failure was published as a hook.action.failed event.
set -uo pipefail

python3 /tests/grade.py
