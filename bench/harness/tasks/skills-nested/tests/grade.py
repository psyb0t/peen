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


NOTES = pathlib.Path("/app/RELEASE_NOTES.md")
EXPECTED = [
    "# Release notes",
    "- [1] Fix login timeout",
    "- [2] Add export button",
    "- [3] Remove legacy cache",
    "-- end of notes --",
]
lines = [line.rstrip() for line in NOTES.read_text().splitlines() if line.strip()] if NOTES.is_file() else []
if lines != EXPECTED:
    failures.append(f"RELEASE_NOTES.md holds {lines}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    delivered = {}
    for frame in trial.of_type(peenaudit.SKILL_INJECTED):
        data = frame.get("data") or {}
        if "<skill>" in (data.get("content") or ""):
            delivered[data.get("name")] = True
    for name in ("release-procedure", "changelog-style"):
        if name not in delivered:
            failures.append(f"skill {name} was never delivered to the model")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
