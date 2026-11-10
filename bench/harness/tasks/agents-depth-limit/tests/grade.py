import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
result = pathlib.Path("/app/out/result.txt")
if not result.is_file() or result.read_text().strip() != "child":
    failures.append("/app/out/result.txt does not hold child")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    runs = trial.query("SELECT id, depth, state FROM agent_runs")
    if not runs:
        failures.append("no child agent ran")
    if any(run["depth"] > 1 for run in runs):
        failures.append("a run deeper than the depth limit was recorded")
    if len(runs) != 1:
        failures.append(f"expected one child run, found {len(runs)}")
    for run in runs:
        if run["state"] != "completed":
            failures.append(f"the child ended {run['state']}")
    nested = [
        call
        for call in trial.calls_named(peenaudit.TOOL_LAUNCH_AGENT)
        if call["agent"] is not None
    ]
    if nested:
        failures.append("a child called launchAgent at the depth limit")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
