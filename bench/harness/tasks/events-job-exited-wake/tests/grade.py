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
woke = pathlib.Path("/app/out/woke.txt")
if not woke.is_file() or woke.read_text() != "woke\n":
    failures.append("/app/out/woke.txt does not hold woke")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    starts = trial.of_type(peenaudit.TURN_STARTED)
    if len(starts) != 2:
        failures.append(f"expected two turns, found {len(starts)}")
    elif (starts[1].get("data") or {}).get("originEventType") != "job.exited":
        failures.append("the second turn was not started by a job.exited event")
    commands = trial.calls_named(peenaudit.TOOL_RUN_COMMAND, include_children=False)
    if not commands or commands[0]["arguments"].get("background") is not True:
        failures.append("the first command was not started in the background")
    turns = trial.query("SELECT completed_at FROM turns ORDER BY started_at")
    jobs = trial.query("SELECT state, ended_at FROM jobs ORDER BY started_at")
    if not jobs or jobs[0]["state"] != "exited":
        failures.append("the background job did not exit on its own")
    elif turns:
        turn_end, job_end = epoch(turns[0]["completed_at"]), epoch(jobs[0]["ended_at"])
        if turn_end is None or job_end is None or turn_end >= job_end:
            failures.append("the first turn did not end before the job did")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
