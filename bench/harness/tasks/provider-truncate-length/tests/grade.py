import pathlib
import re
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []

STATUS = pathlib.Path("/app/status.txt")
TIME_PATTERN = re.compile(
    r"(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2}):(\d{2})(\.\d+)?"
)


def seconds_of(value):
    """Seconds since an arbitrary origin for a stored timestamp, or None."""
    match = TIME_PATTERN.search(str(value))
    if not match:
        return None
    year, month, day, hour, minute, second = (int(p) for p in match.groups()[:6])
    days = year * 372 + month * 31 + day
    return days * 86400 + hour * 3600 + minute * 60 + second + float(match.group(7) or 0)


def duration(row):
    start = seconds_of(row["started_at"])
    end = seconds_of(row["completed_at"])
    if start is None or end is None:
        return None
    return end - start


def check_ended_well(trial):
    if not trial.of_type(peenaudit.TURN_COMPLETED):
        failures.append("the turn did not complete")
    if trial.of_type(peenaudit.TURN_FAILED):
        failures.append("the turn failed")


def retry_frames(trial):
    return [frame["data"] for frame in trial.of_type(peenaudit.PROVIDER_RETRY)]


# The truncated request can be the one that carries the writeFile call, so the
# file is only checked when the model got to write it.
if STATUS.is_file() and STATUS.read_text() != "ready\n":
    failures.append("/app/status.txt does not hold ready")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    check_ended_well(trial)
    rounds = trial.rounds()
    if not any(row["finish_reason"] == "length" for row in rounds):
        failures.append("no round ended with finish_reason length")
    ends = trial.of_type(peenaudit.TURN_COMPLETED)
    if rounds and ends and rounds[-1]["finish_reason"] == "length":
        reported = (ends[-1].get("data") or {}).get("finishReason")
        if reported != "length":
            failures.append(f"turn.completed says finishReason {reported!r}, not length")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
