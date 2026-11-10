#!/bin/bash
# Report the stop.
set -euo pipefail

printf 'stopped\n' >/app/out/status.txt
