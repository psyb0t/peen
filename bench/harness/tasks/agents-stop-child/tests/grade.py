import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
status = pathlib.Path("/app/out/status.txt")
if not status.is_file() or status.read_text() != "stopped\n":
    failures.append("/app/out/status.txt does not hold stopped")


def long_commands():
    found = []
    for entry in pathlib.Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            words = entry.joinpath("cmdline").read_bytes().split(b"\0")
        except OSError:
            continue
        if b"sleep 613" in b" ".join(words):
            found.append(entry.name)
    return found


if long_commands():
    failures.append("the child's sleep 613 is still running")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    runs = trial.query("SELECT id, state FROM agent_runs")
    if len(runs) != 1:
        failures.append(f"expected one child run, found {len(runs)}")
    for run in runs:
        if run["state"] != "cancelled":
            failures.append(f"the child run is {run['state']}, not cancelled")
        stops = [
            call
            for call in trial.calls_named("stopAgent", include_children=False)
            if call["arguments"].get("agentRunId") == run["id"]
        ]
        if not stops:
            failures.append("the root never called stopAgent on the child")
        started = [
            call
            for call in trial.calls_named("runCommand")
            if call["agent"] == run["id"] and "613" in str(call["arguments"])
        ]
        if not started:
            failures.append("the child never started the long command")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
