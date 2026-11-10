import datetime
import re
import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

TIME_PATTERN = re.compile(r"(\d{4})-(\d\d)-(\d\d)[ T](\d\d):(\d\d):(\d\d)(\.\d+)?")


def epoch(value):
    """Seconds since the epoch for a stored timestamp, or None."""
    match = TIME_PATTERN.match(value or "")
    if not match:
        return None
    year, month, day, hour, minute, second = (int(x) for x in match.groups()[:6])
    moment = datetime.datetime(
        year, month, day, hour, minute, second, tzinfo=datetime.timezone.utc
    )
    return moment.timestamp() + float(match.group(7) or 0)


failures = []
result = pathlib.Path("/app/out/result.txt")
if not result.is_file() or result.read_text() != "lantern\n":
    failures.append("/app/out/result.txt does not hold lantern")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    starts = trial.of_type(peenaudit.TURN_STARTED)
    if len(starts) != 2:
        failures.append(f"expected two root turns, found {len(starts)}")
    elif (starts[1].get("data") or {}).get("originEventType") != "agent.finished":
        failures.append("the second turn was not started by an agent.finished event")
    turns = trial.query("SELECT state, completed_at FROM turns ORDER BY started_at")
    runs = trial.query("SELECT state, ended_at FROM agent_runs")
    if len(runs) != 1 or runs[0]["state"] != "completed":
        failures.append("the child did not complete exactly once")
    elif turns:
        first_end, child_end = epoch(turns[0]["completed_at"]), epoch(runs[0]["ended_at"])
        if first_end is None or child_end is None or first_end >= child_end:
            failures.append("the first turn did not end before the child finished")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
