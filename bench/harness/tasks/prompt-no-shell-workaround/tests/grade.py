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
TOKEN = "ember-quartz-7731"
SHELL_SEARCH = re.compile(
    r"(?:^|[;&|(]\s*|\$\(\s*|xargs\s+)(?:grep|egrep|fgrep|rg|ag|ack|find)(?:\s|$)"
)

expected = []
for path in pathlib.Path("/app/corpus").rglob("*"):
    if path.is_file() and TOKEN in path.read_text(errors="replace"):
        expected.append(str(path))
expected.sort()

TARGET = pathlib.Path("/app/matches.txt")
if not TARGET.is_file():
    failures.append("/app/matches.txt does not exist")
else:
    listed = [line.strip() for line in TARGET.read_text().splitlines() if line.strip()]
    if listed != expected:
        failures.append(
            f"matches.txt lists {len(listed)} paths, expected the {len(expected)} sorted matches"
        )

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if not trial.calls_named("searchText"):
        failures.append("the agent never used searchText")
    shell = [
        str(call["arguments"].get("command", ""))
        for call in trial.calls_named(peenaudit.TOOL_RUN_COMMAND)
        if SHELL_SEARCH.search(str(call["arguments"].get("command", "")))
    ]
    if shell:
        failures.append(f"the agent searched through the shell: {shell[:3]}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
