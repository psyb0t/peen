#!/bin/bash
# Grade the route change: routes.txt and the generated file both hold the new
# route, so the generator ran. After a Peen run, no file tool changed anything
# under generated/ successfully and no shell command wrote there directly.
set -uo pipefail

python3 /tests/grade.py
