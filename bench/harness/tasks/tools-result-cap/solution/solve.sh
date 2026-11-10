#!/bin/bash
# Write the sku of the row.
set -euo pipefail

python3 - <<'PY'
import hashlib
open('/app/out/sku.txt', 'w').write('SK-' + hashlib.md5(b'sku4821').hexdigest()[:6].upper() + '\n')
PY
