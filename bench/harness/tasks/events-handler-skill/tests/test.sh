#!/bin/bash
# Grade the skill in a handler body: the stamp file holds the skill's text. After
# a Peen run, the one turn was started by the report.ready event, and the
# skill the handler names reached the model as an injected skill message and
# a skill.injected frame.
set -uo pipefail

python3 /tests/grade.py
