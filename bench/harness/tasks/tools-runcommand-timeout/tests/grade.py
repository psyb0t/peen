import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
path = pathlib.Path("/app/out/done.txt")
if not path.is_file() or path.read_text() != "finished\n":
    failures.append("/app/out/done.txt does not say finished")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    jobs = trial.query(
        "SELECT state, exit_code FROM jobs WHERE command LIKE '%sleep 60%'"
    )
    if len(jobs) != 1:
        failures.append(f"the slow command started {len(jobs)} jobs, 1 expected")
    elif jobs[0]["state"] != "exited" or jobs[0]["exit_code"] != 0:
        failures.append(f"the slow command's job ended as {jobs[0]}")
    calls = trial.tool_calls(include_children=False)
    started = [
        index
        for index, call in enumerate(calls)
        if call["name"] == "runCommand"
        and "sleep 60" in str(call["arguments"].get("command", ""))
    ]
    if not started:
        failures.append("the slow command was never run")
    else:
        first = calls[started[0]]
        if str(first["arguments"].get("timeout", "")).strip() != "10s":
            failures.append("the slow command did not ask for a 10s timeout")
        after = [call["name"] for call in calls[started[0] + 1 :]]
        if not any(name in ("waitJob", "readJobOutput", "listJobs") for name in after):
            failures.append("no job tool followed the slow command")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
