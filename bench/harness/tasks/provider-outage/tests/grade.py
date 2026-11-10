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
    if len(retries) < 5:
        failures.append(f"only {len(retries)} provider.retry frames during the outage")
    failed = trial.of_type(peenaudit.TURN_FAILED)
    completed = trial.of_type(peenaudit.TURN_COMPLETED)
    # Peen may ride out the outage within the turn, or fail the turn and
    # finish on "continue". More than one failed turn is a retry loop.
    if len(failed) > 1:
        failures.append(f"expected at most one failed turn, got {len(failed)}")
    if not completed:
        failures.append("no turn completed after the outage")
    elif failed and completed[-1]["metadata"].get("sequence", 0) < failed[0]["metadata"].get("sequence", 0):
        failures.append("the completed turn came before the failed one")
    rounds = trial.rounds()
    if not any(row["state"] == "failed" and row["failure_classification"] for row in rounds):
        failures.append("the outage left no failed round with a classification")
    if len([m for m in trial.of_type(peenaudit.USER_MESSAGE_CREATED)]) != 2:
        failures.append("the scenario did not send exactly two messages")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
