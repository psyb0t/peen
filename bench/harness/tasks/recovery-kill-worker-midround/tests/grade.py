import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

WORDS = {
    "item1.txt": "AMBER",
    "item2.txt": "BIRCH",
    "item3.txt": "CEDAR",
    "item4.txt": "DELTA",
    "item5.txt": "EMBER",
    "item6.txt": "FJORD",
}
INSPECT_TOOLS = ("searchExecutions", "readExecution")
RESUME_TOOL = "resumeExecution"

failures = []
for name, word in WORDS.items():
    path = pathlib.Path("/app/out", name)
    if not path.is_file() or path.read_text() != word + "\n":
        failures.append(f"/app/out/{name} does not hold {word}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    events = [event.get("event") for event in trial.driver_events()]
    if "worker.killed" not in events:
        failures.append("the worker was never killed, so nothing was recovered")
    if not trial.of_type(peenaudit.TURN_FAILED):
        failures.append("the killed turn was not recorded as failed")
    names = [call["name"] for call in trial.tool_calls(include_children=False)]
    if RESUME_TOOL not in names:
        failures.append("the agent never resumed the retained work")
    else:
        before = names[: names.index(RESUME_TOOL)]
        if not any(name in INSPECT_TOOLS for name in before):
            failures.append("the agent resumed without inspecting the retained work")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
