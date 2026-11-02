#!/bin/bash
# Change the spec and regenerate.
set -euo pipefail

sed -i 's/^greeting=.*/greeting=Hello, Peen/' /app/spec.txt
/app/gen.sh
