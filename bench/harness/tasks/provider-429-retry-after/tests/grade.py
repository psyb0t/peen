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
    if len(retries) != 1:
        failures.append(f"expected one provider.retry frame, got {len(retries)}")
    else:
        retry = retries[0]
        if retry.get("status") != 429:
            failures.append(f"the retry frame status is {retry.get('status')}")
        if retry.get("reason") != "rate_limited":
            failures.append(f"the retry reason is {retry.get('reason')}")
        if retry.get("delayMs", -1) < 2000:
            failures.append(f"the retry delay {retry.get('delayMs')} ms is under 2000")
    rounds = trial.rounds()
    if not rounds:
        failures.append("no root model round was recorded")
    else:
        elapsed = duration(rounds[0])
        if elapsed is None or elapsed < 2.0:
            failures.append(f"the retried round took {elapsed} s, under the 2 s Retry-After")
    check_ended_well(trial)

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
