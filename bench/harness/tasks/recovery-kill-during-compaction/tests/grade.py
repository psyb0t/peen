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
    "1.txt": "orchard",
    "2.txt": "quartz",
    "3.txt": "lantern",
    "4.txt": "meadow",
    "5.txt": "harbor",
    "6.txt": "thistle",
}
check_words("/app/out", {k: v for k, v in WORDS.items()})

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if event_count(trial, "worker.killed") < 1:
        failures.append("the worker was never killed")
    cut = [
        row
        for row in trial.model_calls()
        if row["stage"] == "compaction" and row["state"] != "completed"
    ]
    if not cut:
        failures.append("the kill missed the compaction model call, so no compaction was in flight")
    rows = trial.query("SELECT from_sequence, to_sequence, summary FROM compactions")
    if not rows:
        failures.append("no compaction was stored")
    ranges = [(row["from_sequence"], row["to_sequence"]) for row in rows]
    if len(ranges) != len(set(ranges)):
        failures.append("a compaction range was summarized twice")
    if any(not str(row["summary"]).strip() for row in rows):
        failures.append("a stored compaction has an empty summary")
    done = [
        row
        for row in trial.model_calls()
        if row["stage"] == "compaction" and row["state"] == "completed"
    ]
    if len(done) < len(rows):
        failures.append("a stored compaction has no completed summary call")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
