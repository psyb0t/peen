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


READY = pathlib.Path("/app/ready.txt")
if not READY.is_file() or READY.read_text().strip() != "ready":
    failures.append("ready.txt does not hold the word ready")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    finished = {
        (event.get("detail") or {}).get("label"): event.get("detail") or {}
        for event in trial.driver_events()
        if event.get("event") == "turn.finished"
    }
    first = finished.get("overLimit") or {}
    if first.get("outcome") != "failed" or first.get("code") != "HARNESS_CONFIGURATION_INVALID":
        failures.append(f"the over-limit turn was not refused: {first}")
    second = finished.get("withinLimit") or {}
    if second.get("outcome") != "completed":
        failures.append(f"the turn within the limit did not complete: {second}")
    if len(trial.of_type(peenaudit.TURN_COMPLETED)) != 1:
        failures.append("expected exactly one completed turn")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
