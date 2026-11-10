import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

NUMBERS = ("17", "25", "40")
SUM = "82"

failures = []
total = pathlib.Path("/app/out/sum.txt")
if not total.is_file() or total.read_text() != SUM + "\n":
    failures.append("/app/out/sum.txt does not hold 82")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    runs = trial.query("SELECT id, state FROM agent_runs")
    if len(runs) != 1:
        failures.append(f"expected one child run, found {len(runs)}")
    for run in runs:
        child = run["id"]
        sent = [
            call
            for call in trial.calls_named("sendMessage", include_children=False)
            if call["arguments"].get("to") == child
        ]
        for number in NUMBERS:
            if not any(number in str(call["arguments"].get("message", "")) for call in sent):
                failures.append(f"the root never sent {number} to the child")
        if len({call["callId"] for call in sent}) < len(NUMBERS):
            failures.append("the numbers were not sent in separate calls")
        rows = trial.query(
            "SELECT sequence, content FROM agent_run_messages "
            "WHERE agent_run_id = ? AND role = 'user' AND sequence > 1",
            (child,),
        )
        for number in NUMBERS:
            if not any(number in row["content"] for row in rows):
                failures.append(f"{number} never reached the child's conversation")
        reported = [
            call
            for call in trial.calls_named("sendMessage")
            if call["agent"] == child
            and call["arguments"].get("to") == "root"
            and SUM in str(call["arguments"].get("message", ""))
        ]
        if not reported:
            failures.append("the child never sent the sum to root")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
