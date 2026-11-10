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


STAMP = pathlib.Path("/app/stamp.txt")
if not STAMP.is_file() or STAMP.read_text().strip() != "STAMP:alpha:v3":
    failures.append("stamp.txt does not hold STAMP:alpha:v3")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    launches = [
        call
        for call in trial.calls_named(peenaudit.TOOL_LAUNCH_AGENT, include_children=False)
        if call["arguments"].get("agent") == "stamper"
    ]
    if not launches:
        failures.append("the root never launched the stamper agent")
    results = trial.tool_results()
    loads = [
        call
        for call in trial.calls_named(peenaudit.TOOL_USE_SKILL)
        if call["agent"] is not None
        and call["arguments"].get("name") == "stamp-format"
        and not results.get(call["callId"], {}).get("isError", True)
    ]
    if not loads:
        failures.append("the child never loaded the stamp-format skill")
    writes = [
        call
        for call in trial.tool_calls()
        if call["name"] in peenaudit.MUTATING_TOOLS
        and "/app/stamp.txt" in peenaudit.call_paths(call)
    ]
    if not writes:
        failures.append("no file tool wrote stamp.txt")
    if any(call["agent"] is None for call in writes):
        failures.append("the root wrote stamp.txt instead of the child")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
