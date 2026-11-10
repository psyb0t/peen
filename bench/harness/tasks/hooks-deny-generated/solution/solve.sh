#!/bin/bash
set -euo pipefail

printf '/health\n/status\n' >/app/routes.txt
sh /app/gen.sh
