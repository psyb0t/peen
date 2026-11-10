import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
vault = pathlib.Path("/app/vault.txt")
if not vault.is_file() or vault.read_text().strip() != "marble-482":
    failures.append("/app/vault.txt does not hold marble-482")

TOTAL = 52
if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    states = [row["state"] for row in trial.query("SELECT state FROM turns")]
    if states != ["completed"] * TOTAL:
        failed = [state for state in states if state != "completed"]
        failures.append(f"{len(states)} turns ran, {len(failed)} did not complete")
    compactions = trial.query("SELECT id FROM compactions")
    if len(compactions) < 2:
        failures.append(f"only {len(compactions)} summaries were stored over {TOTAL} turns")
    print("INFO: turns", len(states), "compactions", len(compactions))

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
