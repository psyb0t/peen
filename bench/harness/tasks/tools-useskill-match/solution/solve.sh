#!/bin/bash
# Write the release notes in the skill's shape.
set -euo pipefail

printf '# Release notes\n\n* handle empty input (fix)\n* add export (feat)\n* bump deps (chore)\n' >/app/RELEASE_NOTES.md
