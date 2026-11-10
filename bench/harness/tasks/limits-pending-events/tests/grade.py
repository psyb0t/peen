import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
EXPECTED = [f"entry-{n}" for n in range(1, 10 + 1)]
path = pathlib.Path("/app/events.txt")
if not path.is_file() or path.read_text().splitlines() != EXPECTED:
    failures.append("/app/events.txt does not list every event in order")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    delivered = []
    dropped = 0
    for frame in trial.of_type(peenaudit.SESSION_EVENTS):
        data = frame.get("data") or {}
        dropped += int(data.get("dropped") or 0)
        delivered += [notice.get("summary") for notice in data.get("notices") or []]
    if dropped:
        failures.append(f"the frames report {dropped} dropped events")
    if sorted(delivered) != sorted(EXPECTED):
        failures.append(f"the model was handed {len(delivered)} events, expected 10")
    posted = [e for e in trial.driver_events() if e.get("event") == "notice.posted"]
    if len(posted) != 10 or any((e.get("detail") or {}).get("status", 500) >= 400 for e in posted):
        failures.append("not every event was accepted")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
