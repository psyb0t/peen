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


for name in [f"{kind}{i}" for kind in "ab" for i in range(1, 11)]:
    path = pathlib.Path("/app/chaos", name + ".txt")
    if not path.is_file() or path.read_text() != "status: done\n":
        failures.append(f"{path} does not say status: done")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if event_count(trial, "worker.killed") < 1:
        failures.append("no worker was ever killed")
    if event_count(trial, "controller.killed") < 1:
        failures.append("no controller was ever killed")
    started = [e for e in trial.driver_events() if e.get("event") == "scenario.started"]
    if not started or (started[0].get("detail") or {}).get("seed") != 20261107:
        failures.append("the scenario did not run with the recorded seed")
    open_work = trial.query(
        "SELECT kind, state FROM executions WHERE state IN ('queued', 'running', 'reconciling')"
    )
    if open_work:
        failures.append(f"{len(open_work)} executions are neither terminal nor paused")
    bare = trial.query(
        "SELECT COUNT(*) AS n FROM executions WHERE state = 'paused' AND pause_reason = ''"
    )
    if bare[0]["n"]:
        failures.append("a paused execution has no pause reason")
    unfinished = trial.query("SELECT COUNT(*) AS n FROM turns WHERE state = 'running'")
    if unfinished[0]["n"]:
        failures.append("a turn is still running")
    check = trial.query("PRAGMA integrity_check")
    if [row["integrity_check"] for row in check] != ["ok"]:
        failures.append(f"integrity_check says {check}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
