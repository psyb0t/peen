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


for name, word in (("/app/ok/a.txt", "a"), ("/app/post/c.txt", "c")):
    path = pathlib.Path(name)
    if not path.is_file() or path.read_text().strip() != word:
        failures.append(f"{name} does not hold {word}")
if pathlib.Path("/app/big/b.txt").exists():
    failures.append("/app/big/b.txt exists although its pre hook went over the cap")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    results = trial.tool_results()
    attempted = {}
    for call in trial.calls_named(peenaudit.TOOL_WRITE_FILE):
        for path in peenaudit.call_paths(call):
            attempted[path] = results.get(call["callId"], {})
    if "/app/big/b.txt" not in attempted:
        failures.append("the agent never tried to write /app/big/b.txt")
    elif not attempted["/app/big/b.txt"].get("isError"):
        failures.append("the write of /app/big/b.txt was not refused")
    if attempted.get("/app/ok/a.txt", {}).get("isError"):
        failures.append("the write behind the short hook was refused")
    failed = notices(trial, "hook.action.failed")
    if not any(row["data"].get("hookName") == "big-post" for row in failed):
        failures.append("no hook.action.failed event names the big post hook")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
