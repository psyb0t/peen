#!/bin/bash
# Change the one setting in place.
set -euo pipefail

sed -i 's/^timeout = 30$/timeout = 45/' /app/config/service.ini
