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


DONE = pathlib.Path("/app/done.txt")
if not DONE.is_file() or DONE.read_text().strip() != "done":
    failures.append("done.txt does not hold the word done")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    warnings = []
    for frame in trial.of_type(peenaudit.HARNESS_WARNING):
        warnings += (frame.get("data") or {}).get("warnings") or []
    for directory in ("stale-notes", "misnamed"):
        if not any(
            warning.get("kind") == "skill" and directory in (warning.get("source") or "")
            for warning in warnings
        ):
            failures.append(f"no harness.warning names the {directory} skill")
    if not trial.of_type(peenaudit.TURN_COMPLETED):
        failures.append("the turn did not complete")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
