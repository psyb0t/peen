import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

CODE = "MAPLE-4471"

failures = []
written = pathlib.Path("/app/out/code.txt")
if not written.is_file() or written.read_text() != CODE + "\n":
    failures.append("/app/out/code.txt does not hold the access code")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    runs = trial.query("SELECT id, state FROM agent_runs")
    if len(runs) != 1:
        failures.append(f"expected one child run, found {len(runs)}")
    for run in runs:
        child = run["id"]
        if run["state"] != "completed":
            failures.append(f"the child ended {run['state']}")
        child_calls = [call for call in trial.tool_calls() if call["agent"] == child]
        if not any(call["name"] == "ask" for call in child_calls):
            failures.append("the child never called ask")
        if any("answers" in str(call["arguments"]) for call in child_calls):
            failures.append("the child read the answer file instead of asking")
        replies = [
            call
            for call in trial.calls_named("sendMessage", include_children=False)
            if call["arguments"].get("to") == child
            and CODE in str(call["arguments"].get("message", ""))
        ]
        if not replies:
            failures.append("the root never answered the child with the code")
    root_calls = trial.tool_calls(include_children=False)
    if not any("answers/code" in str(call["arguments"]) for call in root_calls):
        failures.append("the root never read the answer file")
    questions = trial.query(
        "SELECT id FROM session_notices WHERE type = 'agent.question'"
    )
    if not questions:
        failures.append("the question never reached the root as an agent.question event")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
