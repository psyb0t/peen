import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

EXPECTED = "# Release notes\n\n* handle empty input (fix)\n* add export (feat)\n* bump deps (chore)\n"

failures = []
path = pathlib.Path("/app/RELEASE_NOTES.md")
if not path.is_file() or path.read_text().rstrip("\n") != EXPECTED.rstrip("\n"):
    failures.append("RELEASE_NOTES.md is not in the shape the skill describes")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    calls = trial.tool_calls(include_children=False)
    results = trial.tool_results(include_children=False)
    loads = [
        index
        for index, call in enumerate(calls)
        if call["name"] == "useSkill"
        and call["arguments"].get("name") == "release-notes"
        and not results.get(call["callId"], {}).get("isError")
    ]
    changes = [
        index
        for index, call in enumerate(calls)
        if (
            call["name"] in peenaudit.MUTATING_TOOLS
            and any("RELEASE_NOTES" in path for path in peenaudit.call_paths(call))
        )
        or (
            call["name"] == "runCommand"
            and "RELEASE_NOTES" in str(call["arguments"].get("command", ""))
        )
    ]
    if not loads:
        failures.append("the release-notes skill was never loaded with useSkill")
    elif changes and min(changes) < loads[0]:
        failures.append("the notes were written before the skill was loaded")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
