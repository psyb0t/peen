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
    "item1.txt": "FERN",
    "item2.txt": "GLADE",
    "item3.txt": "HERON",
    "item4.txt": "IRIS",
    "item5.txt": "JUNIPER",
    "item6.txt": "KELP",
}
check_words("/app/out", WORDS)

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if event_count(trial, "controller.killed") < 1:
        failures.append("the controller was never killed, so nothing was recovered")
    if event_count(trial, "controller.started") < 2:
        failures.append("the controller was not started again after the kill")
    if not trial.of_type(peenaudit.TURN_FAILED):
        failures.append("the killed turn was not recorded as failed")
    check_inspect_then_resume(trial)
    redone = rewrites(trial, "/app/out/")
    if redone > 1:
        failures.append(f"{redone} output files were written again instead of reusing completed work")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
