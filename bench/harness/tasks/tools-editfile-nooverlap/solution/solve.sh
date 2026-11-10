#!/bin/bash
# Write the changed pipeline line.
set -euo pipefail

printf 'name: nightly\nstages: compile-verify-deploy\nowner: infra\n' >/app/pipeline.txt
