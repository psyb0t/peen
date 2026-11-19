import json
import pathlib
import re
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
import datetime

TARGET = pathlib.Path("/app/today.txt")
today = datetime.datetime.now(datetime.timezone.utc).date()


def near(text):
    try:
        day = datetime.date.fromisoformat(text)
    except ValueError:
        return False
    return abs((day - today).days) <= 1


if not TARGET.is_file():
    failures.append("/app/today.txt does not exist")
else:
    text = TARGET.read_text().strip()
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", text) or not near(text):
        failures.append(f"today.txt does not hold a date near {today}: {text!r}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    prompts = [row["effective_prompt"] for row in trial.query("SELECT effective_prompt FROM prompt_snapshots")]
    dates = [match for prompt in prompts for match in re.findall(r"Today's date \(UTC\): (\d{4}-\d{2}-\d{2})", prompt)]
    if not dates:
        failures.append("no system prompt gives today's date")
    elif not any(near(date) for date in dates):
        failures.append(f"the date in the system prompt is not near {today}: {dates}")

    if any(re.search(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}", prompt) for prompt in prompts):
        failures.append("the system prompt carries a time of day, which breaks prompt caching")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
