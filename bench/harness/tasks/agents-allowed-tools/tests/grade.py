import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
if not pathlib.Path("/app/out/word.txt").is_file() or (
    pathlib.Path("/app/out/word.txt").read_text() != "pine\n"
):
    failures.append("/app/out/word.txt does not hold the word")

ALLOWED = {"listFiles", "readFile", "writeFile"}

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    launches = trial.calls_named(peenaudit.TOOL_LAUNCH_AGENT, include_children=False)
    if not any(call["arguments"].get("agent") == "note-writer" for call in launches):
        failures.append("the root never launched the note-writer agent")
    runs = [
        run
        for run in trial.query("SELECT id, name, definition, state FROM agent_runs")
        if run["name"] == "note-writer"
    ]
    if not runs:
        failures.append("no note-writer run was recorded")
    for run in runs:
        if run["definition"] != "stored" or run["state"] != "completed":
            failures.append(f"note-writer run is {run['definition']}/{run['state']}")
        calls = [call for call in trial.tool_calls() if call["agent"] == run["id"]]
        names = [call["name"] for call in calls]
        if "runCommand" in names:
            failures.append("the child used runCommand despite allowed-tools")
        outside = sorted(
            {
                name
                for name in names
                if name not in ALLOWED and name not in peenaudit.ALWAYS_ALLOWED_TOOLS
            }
        )
        if outside:
            failures.append(f"the child used tools outside allowed-tools: {outside}")
        wrote = [
            call
            for call in calls
            if call["name"] == peenaudit.TOOL_WRITE_FILE
            and "/app/out/word.txt" in peenaudit.call_paths(call)
        ]
        if not wrote:
            failures.append("the child never wrote the file with writeFile")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
