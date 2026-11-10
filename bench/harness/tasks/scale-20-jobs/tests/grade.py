import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
for n in range(1, 21):
    path = pathlib.Path("/app/jobs", f"{n}.txt")
    if not path.is_file() or path.read_text() != f"done {n}\n":
        failures.append(f"/app/jobs/{n}.txt does not hold done {n}")
count = pathlib.Path("/app/count.txt")
if not count.is_file() or count.read_text().strip() != "20":
    failures.append("/app/count.txt does not hold 20")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    jobs = trial.query("SELECT state, exit_code FROM jobs")
    exited = [job for job in jobs if job["state"] == "exited" and job["exit_code"] == 0]
    if len(exited) < 20:
        failures.append(f"{len(exited)} jobs exited with code 0, expected 20")
    running = [job for job in jobs if job["state"] == "running"]
    if running:
        failures.append(f"{len(running)} jobs are still marked running")
    background = [
        call
        for call in trial.calls_named(peenaudit.TOOL_RUN_COMMAND)
        if call["arguments"].get("background") is True
    ]
    if len(background) < 20:
        failures.append(f"{len(background)} commands ran in the background, expected 20")
    if not trial.calls_named("listJobs"):
        failures.append("the agent never called listJobs")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
