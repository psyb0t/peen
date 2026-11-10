import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
alert = pathlib.Path("/app/alert.txt")
if not alert.is_file() or alert.read_text().strip() != "RX-48213":
    failures.append("/app/alert.txt does not hold RX-48213")
end = pathlib.Path("/app/end.txt")
if not end.is_file() or end.read_text().strip() != "harbor":
    failures.append("/app/end.txt does not hold harbor")
if pathlib.Path("/app/data/big.log").stat().st_size < 9_000_000:
    failures.append("big.log was changed")
if pathlib.Path("/app/data/manual.txt").stat().st_size < 2_000_000:
    failures.append("manual.txt was changed")

MAX_RESULT_CHARS = 100000
if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    states = [row["state"] for row in trial.query("SELECT state FROM turns")]
    if states != ["completed"]:
        failures.append(f"turn states were {states}")
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
