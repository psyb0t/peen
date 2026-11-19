#!/bin/bash
# Grade three short turns in one session: each wrote its word. After a Peen
# run, every turn used the same system prompt, and the first model call of
# each later turn read part of its prompt from the provider's cache.
set -uo pipefail

python3 /tests/grade.py
