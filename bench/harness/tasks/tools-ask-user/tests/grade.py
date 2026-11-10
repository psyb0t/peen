import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
path = pathlib.Path("/app/out/codename.txt")
if not path.is_file() or path.read_text().strip() != "blue-heron":
    failures.append("/app/out/codename.txt does not hold the answer")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if not trial.calls_named("ask", include_children=False):
        failures.append("the agent never called ask")
    if not trial.of_type(peenaudit.QUESTION_ASKED):
        failures.append("no question.asked frame was recorded")
    events = [event.get("event") for event in trial.driver_events()]
    for name in ("question.unanswered", "answer.failed"):
        if name in events:
            failures.append(f"the driver logged {name}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
