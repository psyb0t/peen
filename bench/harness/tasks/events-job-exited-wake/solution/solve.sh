#!/bin/bash
# The woken turn writes the handler's file.
set -euo pipefail

printf 'woke\n' >/app/out/woke.txt
