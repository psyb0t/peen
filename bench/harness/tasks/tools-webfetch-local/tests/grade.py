import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

import re

SHELL_FETCH = re.compile(r"\b(?:curl|wget|urllib|requests|nc)\b")

failures = []
first = pathlib.Path("/app/out/first.txt")
if not first.is_file() or first.read_text().strip() != "alpha line":
    failures.append("/app/out/first.txt does not hold the first line of notes.txt")
missing = pathlib.Path("/app/out/missing.txt")
if not missing.is_file() or missing.read_text().strip() != "404":
    failures.append("/app/out/missing.txt does not hold 404")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    results = trial.tool_results(include_children=False)
    fetches = trial.calls_named("webFetch", include_children=False)
    notes = [call for call in fetches if "notes.txt" in str(call["arguments"].get("url", ""))]
    absent = [call for call in fetches if "missing.txt" in str(call["arguments"].get("url", ""))]
    if not notes:
        failures.append("webFetch never read notes.txt")
    if not absent:
        failures.append("webFetch never fetched missing.txt")
    for call in absent:
        result = results.get(call["callId"], {})
        if result.get("isError"):
            failures.append("the 404 came back as a tool error")
        elif "404" not in result.get("content", ""):
            failures.append("the 404 result does not carry the status")
    shell = [
        str(call["arguments"].get("command", ""))
        for call in trial.calls_named("runCommand", include_children=False)
        if SHELL_FETCH.search(str(call["arguments"].get("command", "")))
    ]
    if shell:
        failures.append(f"fetched with the shell: {shell}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
