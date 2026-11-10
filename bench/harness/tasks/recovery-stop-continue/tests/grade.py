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
    "item1.txt": "REED",
    "item2.txt": "SAGE",
    "item3.txt": "TULIP",
    "item4.txt": "UMBEL",
    "item5.txt": "VETCH",
    "item6.txt": "WISTERIA",
    "item7.txt": "YUCCA",
    "item8.txt": "ZINNIA",
    "item9.txt": "ALDER",
    "item10.txt": "BROOM",
}
check_words("/app/out", WORDS)

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if not trial.of_type(peenaudit.TURN_CANCELLED):
        failures.append("the stop did not end the turn as cancelled")
    counts = {}
    for event in trial.driver_events():
        if event.get("event") == "exec.output":
            detail = event.get("detail") or {}
            counts[detail.get("label")] = detail.get("output", "").strip()
    after_stop, after_unrelated = counts.get("afterStop"), counts.get("afterUnrelated")
    if after_stop is None or after_unrelated is None:
        failures.append("the driver did not count the outputs after the stop")
    else:
        if not after_stop.isdigit() or not 0 < int(after_stop) < len(WORDS):
            failures.append(f"{after_stop!r} outputs existed after the stop, so it did not land mid-turn")
        if after_unrelated != after_stop:
            failures.append("the unrelated message moved the stopped work forward")
    frames = trial.frames()
    messages = [i for i, frame in enumerate(frames) if frame["type"] == peenaudit.USER_MESSAGE_CREATED]
    if len(messages) != 3:
        failures.append(f"{len(messages)} user messages reached the session, not three")
    resumes = [
        call["index"]
        for call in trial.tool_calls(include_children=False)
        if call["name"] == RESUME_TOOL
    ]
    if not resumes:
        failures.append("the agent never resumed the stopped work")
    elif len(messages) == 3 and min(resumes) < messages[2]:
        failures.append("the stopped work was resumed before the continue message")
    check_inspect_then_resume(trial)

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
