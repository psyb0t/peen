#!/bin/bash
# List the matching files.
set -euo pipefail

grep -rl 'ember-quartz-7731' /app/corpus | sort >/app/matches.txt
