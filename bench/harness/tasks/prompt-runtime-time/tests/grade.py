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
    prompts = trial.query("SELECT effective_prompt FROM prompt_snapshots")
    stamps = [
        match
        for row in prompts
        for match in re.findall(r"Current local time: (\d{4}-\d{2}-\d{2})T", row["effective_prompt"])
    ]
    if not stamps:
        failures.append("no prompt snapshot carries the runtime time")
    elif not any(near(stamp) for stamp in stamps):
        failures.append(f"the runtime time in the prompt is not near {today}: {stamps}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
