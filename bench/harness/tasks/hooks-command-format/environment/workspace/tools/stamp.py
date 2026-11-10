#!/usr/bin/env python3
"""Prepend the license header to every Go file the hook input names."""

import json
import sys

HEADER = "// Copyright Example Corp"
LOG = "/tmp/stamp-hook.log"

event = json.load(sys.stdin)
paths = event.get("paths") or []
with open(LOG, "a", encoding="utf-8") as log:
    log.write(json.dumps({"event": event.get("event"), "paths": paths}) + "\n")
for path in paths:
    if not path.endswith(".go"):
        continue
    with open(path, encoding="utf-8") as source:
        text = source.read()
    if text.startswith(HEADER + "\n"):
        continue
    with open(path, "w", encoding="utf-8") as target:
        target.write(HEADER + "\n" + text)
