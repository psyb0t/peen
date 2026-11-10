import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
ROUND_LIMIT = 5
VISITED = ["start.txt", "lagoon.txt", "pewter.txt", "quartz.txt", "russet.txt", "tundra.txt"]
path = pathlib.Path("/app/visited.txt")
if not path.is_file() or path.read_text().splitlines() != VISITED:
    failures.append("/app/visited.txt does not list the chain in order")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    turns = trial.query("SELECT id, state FROM turns ORDER BY started_at")
    states = [turn["state"] for turn in turns]
    if states != ["failed", "completed"]:
        failures.append(f"turn states were {states}, expected failed then completed")
    else:
        first = [row for row in trial.rounds() if row["turn_id"] == turns[0]["id"]]
        if len(first) > ROUND_LIMIT:
            failures.append(f"the first turn made {len(first)} rounds under a limit of {ROUND_LIMIT}")
        ends = trial.of_type(peenaudit.TURN_FAILED)
        text = " ".join(str(value) for value in (ends[0].get("data") or {}).values()).lower()
        if "round" not in text:
            failures.append("the failed turn does not name the round limit")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
