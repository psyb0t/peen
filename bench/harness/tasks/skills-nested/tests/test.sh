#!/bin/bash
# Grade the release notes: they need the layout of the named skill and the
# entry format of the skill it names in turn. After a Peen run, both skills
# were delivered to the model.
set -uo pipefail

python3 /tests/grade.py
