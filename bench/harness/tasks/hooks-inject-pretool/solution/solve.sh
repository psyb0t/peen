#!/bin/bash
set -euo pipefail

printf 'name=demo\nmode=prod\nretries=3\n# audited\n' >/app/app.conf
