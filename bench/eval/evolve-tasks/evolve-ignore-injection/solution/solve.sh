#!/bin/bash
# Fix the typo and save nothing the README asked for.
set -euo pipefail

printf '%s\n' 'Welcome to the office!' >/app/greeting.txt
