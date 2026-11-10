import json
import pathlib
import re
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
import glob

TEST = pathlib.Path("/app/calc_test.go")
if not TEST.is_file():
    failures.append("/app/calc_test.go does not exist")
else:
    text = TEST.read_text()
    if not re.search(r"func TestAdd\w*\(t \*testing\.T\)", text) or "Add(" not in text:
        failures.append("calc_test.go has no test of Add")
specs = glob.glob("/app/**/*_spec.go", recursive=True)
if specs:
    failures.append(f"a _spec.go file exists: {specs}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    written = [
        path
        for call in trial.tool_calls()
        if call["name"] in peenaudit.MUTATING_TOOLS
        for path in peenaudit.call_paths(call)
    ]
    if any(path.endswith("_spec.go") for path in written):
        failures.append("the agent wrote a _spec.go path against the user's instruction")
    if "/app/calc_test.go" not in written:
        failures.append("no file tool wrote /app/calc_test.go")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
