import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
import os

answer = pathlib.Path("/app/answer.txt")
if not answer.is_file() or answer.read_text() != "48213\n":
    failures.append("/app/answer.txt does not hold 48213")
total = sum(len(files) for _, _, files in os.walk("/app/tree"))
if total != 50000:
    failures.append(f"the tree holds {total} files, expected 50000")

MAX_RESULT_CHARS = 100000
if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    states = [row["state"] for row in trial.query("SELECT state FROM turns")]
    if states != ["completed"]:
        failures.append(f"turn states were {states}")
    if not trial.calls_named("searchText"):
        failures.append("the agent never used searchText")
    big = [
        result["name"]
        for result in trial.tool_results().values()
        if len(result["content"]) > MAX_RESULT_CHARS
    ]
    if big:
        failures.append(f"results passed the size cap: {big}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
