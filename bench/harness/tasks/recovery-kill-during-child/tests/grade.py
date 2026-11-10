import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []

RESUME_TOOL = "resumeExecution"
INSPECT_TOOLS = ("searchExecutions", "readExecution")


def event_count(trial, name):
    return sum(1 for event in trial.driver_events() if event.get("event") == name)


def rewrites(trial, prefix, agent=False):
    """Extra writes to paths under prefix beyond the first for each path."""
    counts = {}
    for call in trial.tool_calls():
        if call["name"] not in peenaudit.MUTATING_TOOLS:
            continue
        if agent is not False and call["agent"] != agent:
            continue
        for path in peenaudit.call_paths(call):
            if path.startswith(prefix):
                counts[path] = counts.get(path, 0) + 1
    return sum(count - 1 for count in counts.values() if count > 1)


def check_inspect_then_resume(trial):
    names = [call["name"] for call in trial.tool_calls(include_children=False)]
    if RESUME_TOOL not in names:
        failures.append("the agent never resumed the retained work")
        return
    before = names[: names.index(RESUME_TOOL)]
    if not any(name in INSPECT_TOOLS for name in before):
        failures.append("the agent resumed without inspecting the retained work")


def check_words(directory, words, suffix=".txt"):
    for name, word in words.items():
        path = pathlib.Path(directory, name)
        if not path.is_file() or path.read_text() != word + "\n":
            failures.append(f"{path} does not hold {word}")


WORDS = {
    "item1.txt": "ACORN",
    "item2.txt": "BRAMBLE",
    "item3.txt": "CLOVER",
    "item4.txt": "DAISY",
    "item5.txt": "ELM",
    "item6.txt": "FIG",
    "item7.txt": "GROVE",
    "item8.txt": "HAZEL",
}
check_words("/app/out", WORDS)

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if event_count(trial, "worker.killed") < 1:
        failures.append("the worker was never killed, so nothing was recovered")
    launches = trial.calls_named("launchAgent", include_children=False)
    if len(launches) != 1:
        failures.append(f"the root launched {len(launches)} children, not one")
    runs = trial.query("SELECT id, state FROM agent_runs")
    if len(runs) != 1:
        failures.append(f"{len(runs)} agent runs were recorded, not one")
    else:
        run = runs[0]
        if run["state"] != "completed":
            failures.append(f"the child run ended {run['state']}")
        children = trial.query(
            "SELECT state FROM executions WHERE kind = 'child' AND agent_run_id = ?",
            (run["id"],),
        )
        if len(children) != 1 or children[0]["state"] != "completed":
            failures.append("the child's execution did not complete under its original run ID")
        redone = rewrites(trial, "/app/out/", agent=run["id"])
        if redone > 1:
            failures.append(f"the child wrote {redone} outputs again instead of continuing")
        if not [c for c in trial.tool_calls() if c["agent"] == run["id"]]:
            failures.append("the child made no tool calls")
    types = {
        row["event_type"]
        for row in trial.query(
            "SELECT e.event_type FROM execution_events e "
            "JOIN executions x ON x.session_id = e.session_id AND x.id = e.execution_id "
            "WHERE x.kind = 'child'"
        )
    }
    for needed in ("execution.paused", "execution.resumed"):
        if needed not in types:
            failures.append(f"the child execution has no {needed} event")
    check_inspect_then_resume(trial)

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
