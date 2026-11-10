#!/bin/bash
set -euo pipefail

cat >/app/RELEASE_NOTES.md <<'EOF'
# Release notes

- [1] Fix login timeout
- [2] Add export button
- [3] Remove legacy cache

-- end of notes --
EOF
