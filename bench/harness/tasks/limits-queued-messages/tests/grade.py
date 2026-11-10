import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
VALID = {f"done {n}" for n in range(1, 8 + 1)}
path = pathlib.Path("/app/log.txt")
lines = path.read_text().splitlines() if path.is_file() else []
if not lines:
    failures.append("/app/log.txt holds no line")
if any(line not in VALID for line in lines):
    failures.append("/app/log.txt holds a line no message asked for")
if len(set(lines)) != len(lines):
    failures.append("a message was run twice")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    finished = [
        event for event in trial.driver_events() if event.get("event") == "turn.finished"
    ]
    codes = [(event.get("detail") or {}).get("code") for event in finished]
    refused = [code for code in codes if code == "USER_MESSAGE_QUEUE_FULL"]
    other = [code for code in codes if code]
    if len(finished) != 8:
        failures.append(f"{len(finished)} sends finished, expected 8")
    if len(refused) < 4:
        failures.append(f"only {len(refused)} sends were refused as queue full")
    if len(other) != len(refused):
        failures.append("a send failed with a code other than queue full")
    created = len(trial.of_type(peenaudit.USER_MESSAGE_CREATED))
    if created != 8 - len(refused):
        failures.append(f"{created} messages were accepted, expected {8 - len(refused)}")
    started = len(trial.of_type(peenaudit.TURN_STARTED))
    queued = len(trial.of_type("userMessage.queued"))
    if started + queued + len(refused) != 8:
        failures.append(
            f"{started} turns + {queued} queued + {len(refused)} refused do not account for 8 sends"
        )
    print("INFO: refused", len(refused), "queued", queued, "turns", started)

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
