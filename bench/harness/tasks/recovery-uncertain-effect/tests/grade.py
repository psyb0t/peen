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


if not pathlib.Path("/app/ledger.txt").is_file() or pathlib.Path(
    "/app/ledger.txt"
).read_text() != "entry-0\nentry-1\n":
    failures.append("/app/ledger.txt does not hold entry-0 and then entry-1 exactly once")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if event_count(trial, "worker.killed") < 1:
        failures.append("the worker was never killed, so no effect was left uncertain")
    calls = trial.tool_calls(include_children=False)
    names = [call["name"] for call in calls]
    resolves = [call for call in calls if call["name"] == "resolveExecutionActivity"]
    if not resolves:
        failures.append("the agent never recorded a decision for the uncertain step")
    else:
        arguments = resolves[0]["arguments"]
        if len(str(arguments.get("evidence", "")).strip()) < 10:
            failures.append("the decision carries no evidence")
        if arguments.get("resolution") not in ("retry", "accept"):
            failures.append(f"the decision is {arguments.get('resolution')!r}")
        first = names.index("resolveExecutionActivity")
        if not any(name in INSPECT_TOOLS for name in names[:first]):
            failures.append("the agent decided without inspecting the retained work")
        if RESUME_TOOL not in names[first:]:
            failures.append("the agent never resumed the work after its decision")
    retries = sum(1 for call in resolves if call["arguments"].get("resolution") == "retry")
    appends = [
        call
        for call in trial.calls_named("runCommand", include_children=False)
        if ">>" in str(call["arguments"].get("command", ""))
        and "ledger.txt" in str(call["arguments"].get("command", ""))
    ]
    if len(appends) > 1 + retries:
        failures.append(f"the agent ran the append {len(appends)} times by hand")
    left = trial.query("SELECT COUNT(*) AS n FROM execution_activities WHERE state = 'uncertain'")
    if left[0]["n"]:
        failures.append("an activity is still uncertain")
    resolved = trial.query(
        "SELECT COUNT(*) AS n FROM execution_events WHERE event_type = 'activity.resolved'"
    )
    if not resolved[0]["n"]:
        failures.append("no activity.resolved event was recorded")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
