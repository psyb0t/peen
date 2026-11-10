import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
SERIALS = {"rec01.txt": "SER-3FF45714", "rec02.txt": "SER-C344666C", "rec03.txt": "SER-77CCE03B", "rec04.txt": "SER-1EDE1D11", "rec05.txt": "SER-F83BD0D6", "rec06.txt": "SER-8331FA41", "rec07.txt": "SER-16A2B719", "rec08.txt": "SER-57BFFD5B", "rec09.txt": "SER-F04EB949", "rec10.txt": "SER-A86CA0AE", "rec11.txt": "SER-CF40300B", "rec12.txt": "SER-67628B42", "rec13.txt": "SER-5F770AAA", "rec14.txt": "SER-CE4248A7", "rec15.txt": "SER-56A9D059", "rec16.txt": "SER-9FD30DD8", "rec17.txt": "SER-CA6FB0D6", "rec18.txt": "SER-2C264221", "rec19.txt": "SER-F7467E8E", "rec20.txt": "SER-6D52E677", "rec21.txt": "SER-701DEA21", "rec22.txt": "SER-A91E5BD8", "rec23.txt": "SER-E75973E5", "rec24.txt": "SER-11406D45"}
path = pathlib.Path("/app/serials.txt")
if not path.is_file():
    failures.append("/app/serials.txt is missing")
else:
    expected = "".join(f"{name}={serial}\n" for name, serial in SERIALS.items())
    if path.read_text() != expected:
        failures.append("serials.txt does not hold the serial of every record")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    states = [row["state"] for row in trial.query("SELECT state FROM turns")]
    if states != ["completed"]:
        failures.append(f"turn states were {states}")
    runs = trial.query(
        "SELECT state FROM model_runs WHERE stage = ?", (peenaudit.STAGE_COMPACTION,)
    )
    if not runs:
        failures.append("no summary request ran inside the long turn")
    elif not any(run["state"] == "completed" for run in runs):
        failures.append("no summary request completed")
    left_out = [
        record
        for record in peenaudit.read_jsonl("/peen/runtime.log")
        if "dropping the oldest units" in str(record.get("msg", record.get("message", "")))
    ]
    if left_out:
        failures.append(f"{len(left_out)} compactions left history out without a summary")
    print("INFO: summary requests", len(runs))

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
