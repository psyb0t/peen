import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []


LATEST = pathlib.Path("/app/latest.txt")
if not LATEST.is_file() or LATEST.read_text().strip() != "1.9.3":
    failures.append("latest.txt does not hold 1.9.3")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    loaded = [
        call
        for call in trial.calls_named(peenaudit.TOOL_USE_SKILL, include_children=False)
        if call["arguments"].get("name") in ("core:freshness", "freshness")
    ]
    if not loaded:
        failures.append("the agent never loaded the freshness skill")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
