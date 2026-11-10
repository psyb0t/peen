#!/bin/bash
# Write the reviewed report, late entry included.
set -euo pipefail

printf 'status: reviewed\nowner: ops\nitems: 12\nlate-entry: 7\n' >/app/report.txt
