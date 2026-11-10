import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
path = pathlib.Path("/app/out/sum.txt")
if not path.is_file() or path.read_text().strip() != "100":
    failures.append("/app/out/sum.txt does not hold 100")
if pathlib.Path("/app/migrations").exists():
    failures.append("the unrelated skill's migration directory was created")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if "db-migration" in peenaudit.skills_loaded(trial):
        failures.append("the unrelated db-migration skill was loaded")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
