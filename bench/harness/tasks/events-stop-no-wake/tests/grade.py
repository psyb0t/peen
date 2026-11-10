import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
for name in ("finished.txt", "woken.txt"):
    if pathlib.Path("/app/out", name).exists():
        failures.append(f"/app/out/{name} exists")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    starts = trial.of_type(peenaudit.TURN_STARTED)
    if len(starts) != 1:
        failures.append(f"expected one turn, found {len(starts)}")
    if any((frame.get("data") or {}).get("originEventType") for frame in starts):
        failures.append("an event started a turn after the stop")
    if not trial.of_type(peenaudit.TURN_CANCELLED):
        failures.append("the turn was never stopped")
    posted = [
        event
        for event in trial.driver_events()
        if event.get("event") == "notice.posted"
        and (event.get("detail") or {}).get("type") == "late.note"
    ]
    if not posted or posted[0]["detail"].get("status", 500) >= 400:
        failures.append("the event was not accepted while the turn ran")
    if not trial.query("SELECT id FROM session_notices WHERE type = 'late.note'"):
        failures.append("the event was never recorded")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
