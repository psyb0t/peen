#!/bin/bash
# The child could not nest, so it wrote its own word.
set -euo pipefail

printf 'child\n' >/app/out/result.txt
