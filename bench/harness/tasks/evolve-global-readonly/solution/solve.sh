#!/bin/bash
# Write the script and save the rule in the workspace.
set -euo pipefail

printf '#!/bin/bash\n# bench-script\necho hello\n' >/app/hello.sh
chmod +x /app/hello.sh
mkdir -p /app/.agents/learned/rules
cat >/app/.agents/learned/rules/script-header.md <<'RULE'
Every shell script has the comment line `# bench-script` right after the shebang.
RULE
