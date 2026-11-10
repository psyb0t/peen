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


if not STATUS.is_file() or STATUS.read_text() != "ready\n":
    failures.append("/app/status.txt does not hold ready")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    retries = retry_frames(trial)
    if [retry.get("attempt") for retry in retries] != [1, 2, 3]:
        failures.append(f"retry attempts are {[r.get('attempt') for r in retries]}, not 1, 2, 3")
    bounds = {1: (100, 280), 2: (225, 560), 3: (450, 1100)}
    for retry in retries:
        if retry.get("status") != 503:
            failures.append(f"retry {retry.get('attempt')} status is {retry.get('status')}")
        low, high = bounds.get(retry.get("attempt"), (0, 0))
        if not low <= retry.get("delayMs", -1) <= high:
            failures.append(
                f"retry {retry.get('attempt')} delay {retry.get('delayMs')} ms is off the backoff"
            )
    rounds = trial.rounds()
    if not rounds:
        failures.append("no root model round was recorded")
    else:
        first = rounds[0]
        if first["retry_attempt_count"] != 3 or first["total_attempts"] != 4:
            failures.append(
                f"the round recorded {first['retry_attempt_count']} retries "
                f"in {first['total_attempts']} attempts"
            )
        if first["state"] != "completed":
            failures.append(f"the retried round ended {first['state']}")
    check_ended_well(trial)

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
