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


def max_overlap(runs, tolerance=0.05):
    """The most agent runs that were running at the same instant. An end is
    pulled back by the tolerance so bookkeeping order is not counted."""
    marks = []
    for run in runs:
        start, end = epoch(run["started_at"]), epoch(run["ended_at"])
        if start is None or end is None:
            continue
        marks.append((start, 1))
        marks.append((max(start, end - tolerance), -1))
    marks.sort(key=lambda mark: (mark[0], mark[1]))
    running = peak = 0
    for _, change in marks:
        running += change
        peak = max(peak, running)
    return peak


WORDS = ["ALDER", "BIRCH", "CEDAR", "DUNE", "ELM", "FERN", "GORSE", "HEATH"]
CAP = 3

failures = []
for number, word in enumerate(WORDS, start=1):
    path = pathlib.Path("/app/out", f"w{number}.txt")
    if not path.is_file() or path.read_text() != word + "\n":
        failures.append(f"/app/out/w{number}.txt does not hold {word}")


def writes(call, name):
    """True when the call writes the output file name."""
    target = f"/app/out/{name}"
    if call["name"] in peenaudit.MUTATING_TOOLS:
        return target in peenaudit.call_paths(call)
    if call["name"] == peenaudit.TOOL_RUN_COMMAND:
        command = str(call["arguments"].get("command", ""))
        return f"out/{name}" in command and any(
            marker in command for marker in (">", "tee", "cp ", "mv ")
        )
    return False


if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    runs = trial.query("SELECT state, started_at, ended_at FROM agent_runs")
    completed = [run for run in runs if run["state"] == "completed"]
    if len(completed) != len(WORDS) or len(runs) != len(WORDS):
        failures.append(f"expected {len(WORDS)} completed children, found {len(completed)} of {len(runs)}")
    peak = max_overlap(runs)
    if peak > CAP:
        failures.append(f"{peak} children ran at once, above the cap of {CAP}")
    calls = trial.tool_calls()
    for number in range(1, len(WORDS) + 1):
        name = f"w{number}.txt"
        writers = [call for call in calls if writes(call, name)]
        if len(writers) > 1:
            failures.append(f"{name} was written {len(writers)} times")
        if any(call["agent"] is None for call in writers):
            failures.append(f"the root wrote {name} itself")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
