import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

import re

LISTING = re.compile(r"(?:^|[;&|(]\s*|\$\(\s*)(?:ls|find|tree|du)(?:\s|$)")

failures = []
path = pathlib.Path("/app/out/count.txt")
if not path.is_file() or path.read_text().strip() != "20":
    failures.append("/app/out/count.txt does not hold 20")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    lists = trial.calls_named("listFiles")
    if not any(call["arguments"].get("recursive") is True for call in lists):
        failures.append("listFiles was never called with recursive true")
    shell = [
        str(call["arguments"].get("command", ""))
        for call in trial.calls_named("runCommand")
        if LISTING.search(str(call["arguments"].get("command", "")))
    ]
    if shell:
        failures.append(f"listed files with the shell: {shell}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
