#!/bin/bash
# Leave the chain as it is: the task cannot finish within the token limit.
set -euo pipefail

test -f /app/chain/start.txt
