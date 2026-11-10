import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

import collections
import re

FILTER = re.compile(r"[|>]|\b(?:grep|egrep|rg|awk|sed|head|tail)\b")

failures = []
path = pathlib.Path("/app/out/total.txt")
if not path.is_file() or path.read_text().strip() != "48213":
    failures.append("/app/out/total.txt does not hold 48213")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    commands = [
        str(call["arguments"].get("command", "")).strip()
        for call in trial.calls_named("runCommand", include_children=False)
        if "dump.sh" in str(call["arguments"].get("command", ""))
    ]
    raw = [command for command in commands if not FILTER.search(command)]
    narrowed = [command for command in commands if FILTER.search(command)]
    if len(raw) > 2:
        failures.append(f"the unfiltered command ran {len(raw)} times, at most 2 expected")
    if not narrowed:
        failures.append("no later command narrowed the output")
    repeats = {
        command: count
        for command, count in collections.Counter(commands).items()
        if count > 2
    }
    if repeats:
        failures.append(f"the same command ran more than twice: {repeats}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
