import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
stamp = pathlib.Path("/app/out/stamp.txt")
if not stamp.is_file() or stamp.read_text() != "stamp: violet-27\n":
    failures.append("/app/out/stamp.txt does not hold the stamp line")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    starts = trial.of_type(peenaudit.TURN_STARTED)
    if len(starts) != 1:
        failures.append(f"expected one turn, found {len(starts)}")
    elif (starts[0].get("data") or {}).get("originEventType") != "report.ready":
        failures.append("the turn was not started by the report.ready event")
    if "stamp-format" not in peenaudit.skills_loaded(trial):
        failures.append("the handler's skill never reached the model")
    injected = trial.query(
        "SELECT id FROM messages WHERE injected = 1 AND injected_kind = 'skill'"
    )
    if not injected:
        failures.append("no injected skill message was stored")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
