import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

EXPECTED = '[alpha]\nenabled = false\nlevel = 1\n\n[beta]\nenabled = false\nlevel = 2\n\n[gamma]\nenabled = true\nlevel = 3\n\n[delta]\nenabled = false\nlevel = 4\n\n[epsilon]\nenabled = false\nlevel = 5\n'

def refused(trial, name):
    results = trial.tool_results(include_children=False)
    return [
        call
        for call in trial.calls_named(name, include_children=False)
        if results.get(call["callId"], {}).get("isError")
    ]

failures = []
if pathlib.Path("/app/flags.ini").read_text() != EXPECTED:
    failures.append("flags.ini is not the original with only [gamma] enabled")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    rejected = refused(trial, "editFile")
    if len(rejected) > 1:
        failures.append(f"editFile was refused {len(rejected)} times, at most 1 expected")
    changes = [
        call
        for call in trial.tool_calls(include_children=False)
        if call["name"] in peenaudit.MUTATING_TOOLS
    ]
    if not changes:
        failures.append("no file tool changed the file")
    if peenaudit.shell_file_commands(trial, include_children=False):
        failures.append("a shell command was used for the edit")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
