import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
INTERNAL_ERROR_CODE = "INTERNAL_SERVER_ERROR"
ready = pathlib.Path("/app/ready.txt")
if not ready.is_file() or ready.read_text().strip() != "ready":
    failures.append("/app/ready.txt does not hold ready")
if pathlib.Path("/app/oversize.txt").exists():
    failures.append("the oversized message was acted on")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    finished = [
        event for event in trial.driver_events() if event.get("event") == "turn.finished"
    ]
    if len(finished) != 2:
        failures.append(f"{len(finished)} sends finished, expected 2")
    else:
        code = (finished[0].get("detail") or {}).get("code")
        if not code or code == INTERNAL_ERROR_CODE:
            failures.append(f"the oversized message was refused with {code!r}, not a client error code")
    if len(trial.of_type(peenaudit.USER_MESSAGE_CREATED)) != 1:
        failures.append("the oversized message was recorded as accepted")
    turns = trial.query("SELECT state FROM turns")
    if [turn["state"] for turn in turns] != ["completed"]:
        failures.append(f"turn states were {turns}, expected one completed turn")
    stored = trial.query(
        "SELECT count(*) AS n FROM messages WHERE content LIKE '%Padding follows%'"
    )
    if stored[0]["n"]:
        failures.append("the oversized message was stored")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
