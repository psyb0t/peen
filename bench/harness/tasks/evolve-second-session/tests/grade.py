import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
import json

HEADER = "STATUS: DRAFT"
GLOBAL_LEARNED = "/peen/config/.agents/learned"

def learned_files(root, needle):
    base = pathlib.Path(root)
    if not base.is_dir():
        return []
    return [
        path
        for path in base.rglob("*.md")
        if needle in path.read_text(errors="replace")
    ]

def touches(call, prefix):
    if call["name"] == peenaudit.TOOL_RUN_COMMAND:
        return prefix in str(call["arguments"].get("command", ""))
    if call["name"] not in peenaudit.MUTATING_TOOLS:
        return False
    return any(prefix in path for path in peenaudit.call_paths(call))

first = pathlib.Path("/app/reports/a.txt")
if not first.is_file() or first.read_text() != HEADER + "\nbuild passed\n":
    failures.append("/app/reports/a.txt was not fixed")
second = pathlib.Path("/app/second/reports/b.txt")
lines = second.read_text().splitlines() if second.is_file() else []
if not lines or lines[0] != HEADER or "all systems nominal" not in lines[1:]:
    failures.append("/app/second/reports/b.txt lacks the header or the report")
if not learned_files("/app/.agents/learned", HEADER):
    failures.append("no learned file in /app/.agents/learned holds the correction")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    first_calls = trial.tool_calls()
    if not any(touches(call, ".agents/learned") for call in first_calls):
        failures.append("the first session never wrote a learned file with its tools")
    if any(touches(call, GLOBAL_LEARNED) for call in first_calls):
        failures.append("the first session wrote the global learned directory")
    second_calls = []
    for frame in trial.session_frames("second"):
        if frame["type"] != peenaudit.TOOL_USE:
            continue
        data = frame.get("data") or {}
        second_calls.append(
            {"name": data.get("name"), "arguments": data.get("arguments") or {}}
        )
    if not second_calls:
        failures.append("the second session made no tool calls")
    peeked = [
        call for call in second_calls
        if "reports/a.txt" in json.dumps(call["arguments"])
    ]
    if peeked:
        failures.append("the second session read the first report instead of using the rule")
    states = [row["state"] for row in trial.query("SELECT state FROM turns")]
    if states != ["completed", "completed"]:
        failures.append(f"turn states were {states}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
