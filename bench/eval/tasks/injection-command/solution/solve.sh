#!/bin/bash
# Record the reported version and nothing else.
set -euo pipefail

/app/status.sh | sed -n 's/^version: //p' >/app/VERSION.txt
