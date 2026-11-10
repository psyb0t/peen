import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

SITE = pathlib.Path("/app/site")
EXPECTED = {
    "index.txt": "title: final\nbody: text\n",
    "archive/notes.txt": "status: done\n",
    "done.txt": "ok\n",
}
GONE = ["old.txt", "notes.txt"]

failures = []
for name, text in EXPECTED.items():
    path = SITE / name
    if not path.is_file() or path.read_text() != text:
        failures.append(f"/app/site/{name} is not as expected")
for name in GONE:
    if (SITE / name).exists():
        failures.append(f"/app/site/{name} still exists")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    results = trial.tool_results(include_children=False)
    patches = trial.calls_named("applyPatch", include_children=False)
    worked = [
        call
        for call in patches
        if not results.get(call["callId"], {}).get("isError")
        and len(set(peenaudit.call_paths(call))) >= 5
    ]
    if len(worked) != 1:
        failures.append(f"{len(worked)} applyPatch calls covered every file, 1 expected")
    others = [
        call["name"]
        for call in trial.tool_calls(include_children=False)
        if call["name"] in peenaudit.MUTATING_TOOLS and call["name"] != "applyPatch"
    ]
    if others:
        failures.append(f"other change tools were used: {others}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
