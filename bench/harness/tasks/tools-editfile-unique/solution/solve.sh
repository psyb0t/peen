#!/bin/bash
# Flip the one flag in place.
set -euo pipefail

python3 - <<'PY'
import pathlib
path = pathlib.Path('/app/flags.ini')
path.write_text(path.read_text().replace('[gamma]\nenabled = false', '[gamma]\nenabled = true'))
PY
