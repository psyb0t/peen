import json
import pathlib
import re
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
import glob

TARGET = pathlib.Path("/app/reports/summary.md")
if not TARGET.is_file() or TARGET.read_text().strip() != "done":
    failures.append("/app/reports/summary.md does not hold done")
others = [
    path
    for path in glob.glob("/app/**/summary*", recursive=True)
    if path != str(TARGET)
]
if others:
    failures.append(f"other summary files exist: {others}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    calls = trial.tool_calls(include_children=False)
    hops = [
        call
        for call in calls
        if call["name"] == peenaudit.TOOL_CHANGE_DIRECTORY
        and peenaudit.normalize_path(call["arguments"].get("directory")) == "/app/reports"
    ]
    writes = [
        call
        for call in calls
        if (
            call["name"] in peenaudit.MUTATING_TOOLS
            and any("summary" in path for path in peenaudit.call_paths(call))
        )
        or (
            call["name"] == peenaudit.TOOL_RUN_COMMAND
            and "summary" in str(call["arguments"].get("command", ""))
        )
    ]
    if not hops:
        failures.append("the agent never changed into /app/reports")
    elif writes and writes[0]["index"] < hops[0]["index"]:
        failures.append("the report was written before the agent entered reports")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
