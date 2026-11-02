#!/bin/bash
# Add the setting and keep everything else.
set -euo pipefail

python3 - <<'EOF'
import json

path = "/app/config/settings.json"
with open(path) as handle:
    settings = json.load(handle)

settings["requestTimeoutSeconds"] = 30

with open(path, "w") as handle:
    json.dump(settings, handle, indent=2)
    handle.write("\n")
EOF
