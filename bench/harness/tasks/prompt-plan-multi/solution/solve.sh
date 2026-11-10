#!/bin/bash
# Rename in all four files.
set -euo pipefail

sed -i 's/fetch_user/load_user/g' /app/users.py /app/api.py /app/cli.py /app/jobs.py
