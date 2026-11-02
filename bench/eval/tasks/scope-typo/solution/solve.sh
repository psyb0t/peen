#!/bin/bash
# Fix the one misspelled word.
set -euo pipefail

sed -i 's/recieved/received/' /app/greet/greet.go
