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


def hook_block_seen(trial, marker):
    """True when a stored record holds a hook block carrying the marker."""
    queries = (
        "SELECT content AS text FROM messages",
        "SELECT response_injections_json AS text FROM model_runs",
        "SELECT payload_json AS text FROM events",
    )
    for sql in queries:
        for row in trial.query(sql):
            text = row["text"] or ""
            if "<hook>" in text and marker in text:
                return True
    return False


CONF = pathlib.Path("/app/app.conf")
lines = [line.rstrip() for line in CONF.read_text().splitlines() if line.strip()] if CONF.is_file() else []
if lines != ["name=demo", "mode=prod", "retries=3", "# audited"]:
    failures.append(f"app.conf holds {lines}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if not hook_block_seen(trial, "# audited"):
        failures.append("no hook block with the audit policy was recorded")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
