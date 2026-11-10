import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
count = pathlib.Path("/app/out/count.txt")
if not count.is_file() or count.read_text() != "7\n":
    failures.append("/app/out/count.txt does not hold 7")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    launches = trial.calls_named(peenaudit.TOOL_LAUNCH_AGENT, include_children=False)
    inline = [
        call
        for call in launches
        if isinstance(call["arguments"].get("agentDefinition"), dict)
        and not call["arguments"].get("agent")
    ]
    if not inline:
        failures.append("no launchAgent call used agentDefinition alone")
    else:
        definition = inline[0]["arguments"]["agentDefinition"]
        runs = trial.query(
            "SELECT id, name, definition, state, instructions FROM agent_runs"
        )
        matching = [run for run in runs if run["name"] == definition.get("name")]
        if not matching:
            failures.append("no run was recorded under the inline agent's name")
        for run in matching:
            if run["definition"] != "ad-hoc":
                failures.append(f"run definition is {run['definition']}, not ad-hoc")
            if str(definition.get("instructions", "")).strip() not in run["instructions"]:
                failures.append("the run did not record the inline instructions")
            if run["state"] != "completed":
                failures.append(f"the inline child ended {run['state']}")
            if not any(call["agent"] == run["id"] for call in trial.tool_calls()):
                failures.append("the inline child made no tool call")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
