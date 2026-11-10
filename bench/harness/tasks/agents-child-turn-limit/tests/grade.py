import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
result = pathlib.Path("/app/out/result.txt")
if not result.is_file() or result.read_text() != "HARBOR LANTERN\n":
    failures.append("/app/out/result.txt does not hold HARBOR LANTERN")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    runs = trial.query("SELECT id, state FROM agent_runs")
    if not runs:
        failures.append("no child agent ran")
    if not any(run["state"] == "failed" for run in runs):
        failures.append("no child run failed at the round limit")
    # The failure reaches the root as an agent.failed event, or, while the
    # root waits for messages, as a failed message from the child.
    notices = trial.query("SELECT id FROM session_notices WHERE type = 'agent.failed'")
    told = trial.query(
        "SELECT id FROM messages WHERE role = 'user' AND content LIKE ?",
        ('%<message from="agent"%kind="failed"%',),
    )
    if not notices and not told:
        failures.append("the child's failure never reached the root")
    root_wrote = [
        call
        for call in trial.tool_calls(include_children=False)
        if (
            call["name"] in peenaudit.MUTATING_TOOLS
            and "/app/out/result.txt" in peenaudit.call_paths(call)
        )
        or (
            call["name"] == peenaudit.TOOL_RUN_COMMAND
            and "result.txt" in str(call["arguments"].get("command", ""))
        )
    ]
    if not root_wrote:
        failures.append("the root never wrote the result itself")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
