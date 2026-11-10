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


TAGS = {1: "cedar", 2: "delta", 3: "ember", 4: "fjord"}
for part, tag in TAGS.items():
    path = pathlib.Path(f"/app/out/{part}.txt")
    if not path.is_file() or path.read_text().strip() != f"END part {part}: {tag}":
        failures.append(f"{path} does not hold the last line of part {part}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    compactions = trial.query("SELECT id FROM compactions")
    checkpoints = trial.query(
        "SELECT step_id FROM execution_activities WHERE name = 'compactionHook'"
    )
    if not compactions and not checkpoints:
        failures.append("no compaction ran, so the fixture did not outgrow the context")
    log = pathlib.Path("/tmp/compact-hooks.log")
    text = log.read_text() if log.is_file() else ""
    pre = len(re.findall(r'"event"\s*:\s*"preCompact"', text))
    post = len(re.findall(r'"event"\s*:\s*"postCompact"', text))
    if pre < 1:
        failures.append("the preCompact hook never ran")
    if post < 1:
        failures.append("the postCompact hook never ran")
    if pre != post:
        failures.append(f"preCompact ran {pre} times and postCompact {post}")
    if len(checkpoints) < 2:
        failures.append("the saved checkpoints do not show both compaction hooks")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
