#!/bin/bash
set -euo pipefail

cat >/app/scripts/clean.sh <<'EOF'
#!/bin/bash
# owner: platform-team
set -eu
find "$1" -type f -name '*.tmp' -delete
EOF
chmod +x /app/scripts/clean.sh
