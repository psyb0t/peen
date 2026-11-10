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


EXPECTED = {"a.txt": "AMBER", "b.txt": "BIRCH", "c.txt": "CEDAR", "d.txt": "DELTA"}
CAP = 2

failures = []
for name, word in EXPECTED.items():
    path = pathlib.Path("/app/out", name)
    if not path.is_file() or path.read_text() != word + "\n":
        failures.append(f"/app/out/{name} does not hold {word}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    runs = trial.query("SELECT state, started_at, ended_at FROM agent_runs")
    completed = [run for run in runs if run["state"] == "completed"]
    if len(completed) < len(EXPECTED):
        failures.append(f"only {len(completed)} children completed")
    if len(completed) != len(runs):
        failures.append("a child ended in a state other than completed")
    peak = max_overlap(runs)
    if peak > CAP:
        failures.append(f"{peak} children ran at once, above the cap of {CAP}")
    results = trial.tool_results(include_children=False)
    refused = [
        call
        for call in trial.calls_named(peenaudit.TOOL_LAUNCH_AGENT, include_children=False)
        if "too many concurrent agent runs"
        in results.get(call["callId"], {}).get("content", "").lower()
    ]
    if not refused:
        failures.append("no launch was refused for the concurrent run cap")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
