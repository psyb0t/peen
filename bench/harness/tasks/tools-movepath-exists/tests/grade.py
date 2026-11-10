import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

import re

SHELL_MOVE = re.compile(r"(?:^|[;&|(]\s*|\$\(\s*)(?:sudo\s+)?(?:mv|cp|rm|rsync|install|ln)(?:\s|$)")

failures = []
expected = {"/app/b/report.txt": "from-a\n", "/app/b/report.old.txt": "from-b\n"}
for name, text in expected.items():
    path = pathlib.Path(name)
    if not path.is_file() or path.read_text() != text:
        failures.append(f"{name} is not as expected")
if pathlib.Path("/app/a/report.txt").exists():
    failures.append("/app/a/report.txt still exists")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    results = trial.tool_results(include_children=False)
    moves = trial.calls_named("movePath", include_children=False)
    if not moves:
        failures.append("movePath was never called")
    rejected = [call for call in moves if results.get(call["callId"], {}).get("isError")]
    if len(rejected) > 1:
        failures.append(f"movePath was refused {len(rejected)} times, at most 1 expected")
    shell = [
        str(call["arguments"].get("command", ""))
        for call in trial.calls_named("runCommand", include_children=False)
        if SHELL_MOVE.search(str(call["arguments"].get("command", "")))
    ]
    if shell:
        failures.append(f"moved files with the shell: {shell}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
