import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
one = pathlib.Path("/app/out/one.txt")
if not one.is_file() or one.read_text().strip() != "stamp 7f3a":
    failures.append("/app/out/one.txt does not hold the stamp line")
if pathlib.Path("/app/out/two.txt").exists():
    failures.append("/app/out/two.txt exists, the unknown skill message ran")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    injected = [
        (frame.get("data") or {}).get("name") for frame in trial.of_type(peenaudit.SKILL_INJECTED)
    ]
    if injected != ["stamp-file"]:
        failures.append(f"skill.injected frames named {injected}, expected one for stamp-file")
    finished = {
        (event.get("detail") or {}).get("label"): event.get("detail") or {}
        for event in trial.driver_events()
        if event.get("event") == "turn.finished"
    }
    second = finished.get("unknown")
    if second is None:
        failures.append("the unknown skill message was never sent")
    elif second.get("outcome") != "failed" or second.get("code") != "VALIDATION_FAILED":
        failures.append(f"the unknown skill message ended as {second}")
    turns = trial.query("SELECT COUNT(*) AS n FROM turns")[0]["n"]
    if turns != 1:
        failures.append(f"{turns} turns were stored, 1 expected")
    turn_ids = {row["turn_id"] for row in trial.model_calls() if row["turn_id"]}
    if len(turn_ids) != 1:
        failures.append(f"model calls belong to {len(turn_ids)} turns, 1 expected")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
