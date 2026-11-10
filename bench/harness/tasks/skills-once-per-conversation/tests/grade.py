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


for name, word in (("one", "one"), ("two", "two")):
    path = pathlib.Path("/app", name + ".txt")
    lines = [line.strip() for line in path.read_text().splitlines() if line.strip()] if path.is_file() else []
    if lines != ["HEADER-7", word]:
        failures.append(f"{path} holds {lines}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    full = []
    notes = []
    for frame in trial.of_type(peenaudit.SKILL_INJECTED):
        data = frame.get("data") or {}
        content = data.get("content") or ""
        if "<skill>" in content:
            full.append(frame)
        elif "already loaded" in content:
            notes.append(frame)
    if len(full) != 1:
        failures.append(f"the skill was delivered in full {len(full)} times, expected 1")
    if len(notes) != 1:
        failures.append(f"expected one already-loaded note, found {len(notes)}")
    if full and notes and peenaudit.sequence_of(notes[0]) is not None and peenaudit.sequence_of(full[0]) is not None:
        if peenaudit.sequence_of(notes[0]) < peenaudit.sequence_of(full[0]):
            failures.append("the note came before the skill")
    ends = trial.of_type(peenaudit.TURN_COMPLETED)
    if len(ends) < 2:
        failures.append("fewer than two turns completed")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
