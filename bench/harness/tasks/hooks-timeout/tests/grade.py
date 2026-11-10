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


EXPECTED = {
    "/app/a/one.txt": None,
    "/app/b/two.txt": "two",
    "/app/c/three.txt": "three",
    "/app/d/four.txt": None,
}
for name, word in EXPECTED.items():
    path = pathlib.Path(name)
    if word is None:
        if path.exists():
            failures.append(f"{name} exists although its pre hook timed out")
    elif not path.is_file() or path.read_text().strip() != word:
        failures.append(f"{name} does not hold {word}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    results = trial.tool_results()
    attempted = {}
    for call in trial.calls_named(peenaudit.TOOL_WRITE_FILE):
        for path in peenaudit.call_paths(call):
            attempted[path] = results.get(call["callId"], {})
    for name in ("/app/a/one.txt", "/app/d/four.txt"):
        if name not in attempted:
            failures.append(f"the agent never tried to write {name}")
        elif not attempted[name].get("isError"):
            failures.append(f"the write of {name} was not refused")
    failed = notices(trial, "hook.action.failed")
    if not any(row["data"].get("hookName") == "slow-post" for row in failed):
        failures.append("no hook.action.failed event names the slow post hook")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
