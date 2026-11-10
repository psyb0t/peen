import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
for number in range(1, 6):
    path = pathlib.Path("/app/out", f"tick-{number}.txt")
    if not path.is_file() or path.read_text() != f"{number}\n":
        failures.append(f"/app/out/tick-{number}.txt does not hold {number}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    frames = trial.frames()
    wakes = [
        frame
        for frame in trial.of_type(peenaudit.TURN_STARTED)
        if (frame.get("data") or {}).get("originEventType") == "tick.fired"
    ]
    plain = [
        frame
        for frame in trial.of_type(peenaudit.TURN_STARTED)
        if not (frame.get("data") or {}).get("originEventType")
    ]
    if len(wakes) != 3:
        failures.append(f"expected 3 wake turns, found {len(wakes)}")
    if len(plain) != 1:
        failures.append(f"expected 1 turn from the user's message, found {len(plain)}")
    notices = trial.query(
        "SELECT summary, state FROM session_notices WHERE type = 'tick.fired'"
    )
    if len(notices) != 5 or any(row["state"] != "delivered" for row in notices):
        failures.append("not all five tick events were recorded and delivered")
    # The events the user's turn delivered are the ones the cap held back.
    # A turn's opening events can be stored before its turn.started frame,
    # so they are matched by the turn's request ID, not by position.
    plain_requests = {
        (frame.get("metadata") or {}).get("requestId") for frame in plain
    }
    late = [
        notice.get("summary", "")
        for frame in frames
        if frame["type"] == peenaudit.SESSION_EVENTS
        and (frame.get("metadata") or {}).get("requestId") in plain_requests
        for notice in (frame.get("data") or {}).get("notices") or []
        if notice.get("type") == "tick.fired"
    ]
    if len(late) != 2:
        failures.append(f"expected 2 events delivered later, found {len(late)}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
