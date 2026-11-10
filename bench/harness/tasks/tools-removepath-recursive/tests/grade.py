import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

import re

SHELL_REMOVE = re.compile(r"(?:^|[;&|(]\s*|\$\(\s*)(?:sudo\s+)?(?:rm|rmdir|unlink|find)(?:\s|$)")

failures = []
if pathlib.Path("/app/scratch").exists():
    failures.append("/app/scratch still exists")
keep = pathlib.Path("/app/keep/important.txt")
if not keep.is_file() or keep.read_text() != "keep me\n":
    failures.append("/app/keep/important.txt was changed or removed")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    results = trial.tool_results(include_children=False)
    removes = [
        call
        for call in trial.calls_named("removePath", include_children=False)
        if peenaudit.normalize_path(call["arguments"].get("path")) == "/app/scratch"
    ]
    if not any(call["arguments"].get("recursive") is True for call in removes):
        failures.append("removePath was never called on /app/scratch with recursive true")
    rejected = [call for call in removes if results.get(call["callId"], {}).get("isError")]
    if len(rejected) > 1:
        failures.append(f"removePath was refused {len(rejected)} times, at most 1 expected")
    shell = [
        str(call["arguments"].get("command", ""))
        for call in trial.calls_named("runCommand", include_children=False)
        if SHELL_REMOVE.search(str(call["arguments"].get("command", "")))
    ]
    if shell:
        failures.append(f"removed files with the shell: {shell}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
