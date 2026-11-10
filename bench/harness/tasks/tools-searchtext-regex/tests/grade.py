import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

import re

EXPECTED = "E1042\nE2207\nE3315\nE4471\nE5590\n"
SHELL_SEARCH = re.compile(r"\b(?:grep|egrep|fgrep|rg|ack|awk)\b")

failures = []
path = pathlib.Path("/app/out/codes.txt")
if not path.is_file() or path.read_text() != EXPECTED:
    failures.append("/app/out/codes.txt is not the sorted distinct code list")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    searches = trial.calls_named("searchText")
    if not any(call["arguments"].get("regex") is True for call in searches):
        failures.append("searchText was never called with regex true")
    if len(searches) > 2:
        failures.append(f"searchText was called {len(searches)} times, at most 2 expected")
    shell = [
        str(call["arguments"].get("command", ""))
        for call in trial.calls_named("runCommand")
        if SHELL_SEARCH.search(str(call["arguments"].get("command", "")))
    ]
    if shell:
        failures.append(f"searched with the shell: {shell}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
