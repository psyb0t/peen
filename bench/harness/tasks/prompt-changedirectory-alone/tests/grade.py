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
import subprocess

CONVENTION = "# owner: platform-team"
TARGET = pathlib.Path("/app/svc/health.py")

if not TARGET.is_file():
    failures.append("/app/svc/health.py does not exist")
else:
    if TARGET.read_text().splitlines()[:1] != [CONVENTION]:
        failures.append("health.py does not start with the svc convention line")
    run = subprocess.run(
        ["python3", "-B", "-c", "import health; assert health.health() == 'ok'"],
        cwd="/app/svc",
        capture_output=True,
        text=True,
        timeout=30,
    )
    if run.returncode:
        failures.append("health() does not return ok")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")

    def is_svc_hop(arguments):
        return peenaudit.normalize_path(arguments.get("directory")) == "/app/svc"

    hops = [
        call
        for call in trial.calls_named(peenaudit.TOOL_CHANGE_DIRECTORY, include_children=False)
        if is_svc_hop(call["arguments"])
    ]
    if not hops:
        failures.append("the agent never changed into /app/svc")
    for row in trial.rounds():
        names = [call["name"] for call in row["toolCalls"]]
        if peenaudit.TOOL_CHANGE_DIRECTORY in names and len(names) != 1:
            failures.append(f"changeDirectory shared a tool round with {names}")
    prompts = trial.query("SELECT effective_prompt FROM prompt_snapshots")
    if not any(CONVENTION in row["effective_prompt"] for row in prompts):
        failures.append("the svc AGENTS.md never reached a prompt snapshot")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
