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
TARGET = pathlib.Path("/app/big/done.txt")
if not TARGET.is_file() or TARGET.read_text().strip() != "ok":
    failures.append("/app/big/done.txt does not hold ok")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    results = trial.tool_results(include_children=False)
    hops = [
        call
        for call in trial.calls_named(peenaudit.TOOL_CHANGE_DIRECTORY, include_children=False)
        if peenaudit.normalize_path(call["arguments"].get("directory")) == "/app/big"
    ]
    if not hops:
        failures.append("the agent never tried to change into /app/big")
    elif not any(results.get(call["callId"], {}).get("isError") for call in hops):
        failures.append("the directory change into the oversized scope was not refused")
    if not trial.of_type(peenaudit.TURN_COMPLETED):
        failures.append("the turn did not complete")
    if trial.of_type(peenaudit.TURN_FAILED):
        failures.append("a turn failed")
    if trial.of_type("message.failed"):
        failures.append("a message was refused")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
