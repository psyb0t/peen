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
    "item1.txt": "BASIL",
    "item2.txt": "CHIVE",
    "item3.txt": "DILL",
    "item4.txt": "FENNEL",
    "item5.txt": "GARLIC",
    "item6.txt": "HYSSOP",
}
check_words("/app/out", WORDS)

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    live = peenaudit.read_jsonl("/peen/live-1.jsonl")
    first = [r["sequence"] for r in live if r.get("phase") == "first" and r.get("sequence") is not None]
    again = [r["sequence"] for r in live if r.get("phase") == "reattach" and r.get("sequence") is not None]
    if not first or not again:
        failures.append(f"the live file holds {len(first)} first and {len(again)} reattach frames")
    else:
        joined = first + again
        if len(joined) != len(set(joined)):
            failures.append("a sequence appears twice across the two phases")
        if min(again) <= max(first):
            failures.append("the reattached feed repeated sequences the first socket saw")
        main = {peenaudit.sequence_of(frame) for frame in trial.frames()} - {None}
        wanted = {s for s in main if min(joined) <= s <= max(joined)}
        if set(joined) != wanted:
            lost = sorted(wanted - set(joined))[:5]
            extra = sorted(set(joined) - wanted)[:5]
            failures.append(f"the joined phases differ from the stored feed: lost {lost}, extra {extra}")
    if not any(r.get("phase") == "reattach" and r.get("type") in peenaudit.TURN_ENDS for r in live):
        failures.append("the reattached feed never delivered the turn's end")
    if event_count(trial, "socket.reattached") != 1:
        failures.append("the driver did not reattach exactly once")
    if len(trial.of_type(peenaudit.TURN_COMPLETED)) != 1 or trial.of_type(peenaudit.TURN_FAILED, peenaudit.TURN_CANCELLED):
        failures.append("the disconnect changed how the turn ended")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
