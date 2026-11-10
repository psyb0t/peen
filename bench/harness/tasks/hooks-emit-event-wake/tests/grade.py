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


def notices(trial, notice_type):
    """Stored session notices of one type, with their data decoded."""
    found = []
    for row in trial.query(
        "SELECT source, summary, data_json FROM session_notices WHERE type = ?",
        (notice_type,),
    ):
        row["data"] = json.loads(row["data_json"] or "{}")
        found.append(row)
    return found


REPORT = pathlib.Path("/app/report.txt")
PROCESSED = pathlib.Path("/app/processed.txt")
if not REPORT.is_file() or REPORT.read_text().strip() != "report":
    failures.append("report.txt does not hold the word report")
if not PROCESSED.is_file() or PROCESSED.read_text().strip() != "wake handled":
    failures.append("processed.txt does not hold wake handled")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    found = notices(trial, "session.wrapup.ready")
    if not found:
        failures.append("the hook's event was never stored")
    elif found[0]["source"] != "hooks":
        failures.append(f"the event source is {found[0]['source']}, expected hooks")
    handlers = trial.query(
        "SELECT content FROM messages WHERE role = 'user' AND content LIKE '<handler>%'"
    )
    if not any("session.wrapup.ready" in row["content"] for row in handlers):
        failures.append("no handler message started a woken turn")
    if len(trial.of_type(peenaudit.TURN_COMPLETED)) < 2:
        failures.append("fewer than two turns completed")
    sends = [
        event
        for event in trial.driver_events()
        if event.get("event") == "step.started"
        and (event.get("detail") or {}).get("op") == "send"
    ]
    if len(sends) != 1:
        failures.append(f"the driver sent {len(sends)} messages, expected 1")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
