import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
ok = pathlib.Path("/app/out/ok.txt")
if not ok.is_file() or ok.read_text() != "ok\n":
    failures.append("/app/out/ok.txt does not hold ok")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    posted = [
        event
        for event in trial.driver_events()
        if event.get("event") == "notice.posted"
        and (event.get("detail") or {}).get("type") == "job.exited"
    ]
    if not posted:
        failures.append("the driver never posted the reserved event")
    elif posted[0]["detail"].get("status") != 400:
        failures.append(f"the reserved event was answered {posted[0]['detail'].get('status')}, not 400")
    if trial.query("SELECT id FROM session_notices"):
        failures.append("a notice was recorded")
    if trial.of_type("notice.created"):
        failures.append("a notice.created frame was emitted")
    if len(trial.of_type(peenaudit.TURN_STARTED)) != 1:
        failures.append("a turn other than the user's started")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
