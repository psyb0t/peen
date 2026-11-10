#!/bin/bash
# Mark every file done.
set -euo pipefail

sed -i 's/pending/done/' /app/chaos/*.txt
