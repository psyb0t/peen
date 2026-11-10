#!/bin/bash
# Grade the message exchange: the sum file holds 82. After a Peen run, the root
# sent each number to the child in its own sendMessage call, each reached the
# child's conversation as a separate message, and the child reported 82 to root.
set -uo pipefail

python3 /tests/grade.py
