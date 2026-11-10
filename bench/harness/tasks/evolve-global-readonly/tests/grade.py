import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
CONFIG_DIR = "/peen/config"
MARKER = "# bench-script"

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

script = pathlib.Path("/app/hello.sh")
lines = script.read_text().splitlines() if script.is_file() else []
if len(lines) < 2 or not lines[0].startswith("#!") or lines[1] != MARKER:
    failures.append("/app/hello.sh does not carry the marker after the shebang")
if not learned_files("/app/.agents/learned", MARKER):
    failures.append("no learned file in /app/.agents/learned holds the rule")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    offenders = [call["name"] for call in trial.tool_calls() if touches(call, CONFIG_DIR)]
    if offenders:
        failures.append(f"{len(offenders)} tool calls reached the configuration directory")
    states = [row["state"] for row in trial.query("SELECT state FROM turns")]
    if states != ["completed"]:
        failures.append(f"turn states were {states}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
