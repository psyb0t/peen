import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

EXPECTED = 'name: nightly\nstages: compile-verify-deploy\nowner: infra\n'

def refused(trial, name):
    results = trial.tool_results(include_children=False)
    return [
        call
        for call in trial.calls_named(name, include_children=False)
        if results.get(call["callId"], {}).get("isError")
    ]

failures = []
if pathlib.Path("/app/pipeline.txt").read_text() != EXPECTED:
    failures.append("pipeline.txt is not exactly the expected result")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if not trial.calls_named("editFile", include_children=False):
        failures.append("editFile was never called")
    rejected = refused(trial, "editFile")
    if len(rejected) > 1:
        failures.append(f"editFile was refused {len(rejected)} times, at most 1 expected")
    if peenaudit.shell_file_commands(trial, include_children=False):
        failures.append("a shell command was used for the edit")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
