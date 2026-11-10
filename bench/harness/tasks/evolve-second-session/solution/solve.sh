#!/bin/bash
# Fix the report, save the rule, and write the second report.
set -euo pipefail

printf 'STATUS: DRAFT\nbuild passed\n' >/app/reports/a.txt
mkdir -p /app/.agents/learned/rules
cat >/app/.agents/learned/rules/report-header.md <<'RULE'
Every report file in this project must begin with the line `STATUS: DRAFT`.
RULE
printf 'STATUS: DRAFT\nall systems nominal\n' >/app/second/reports/b.txt
