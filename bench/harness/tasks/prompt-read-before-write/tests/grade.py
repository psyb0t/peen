import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

TARGET = "/app/config/service.ini"
EXPECTED = """[service]
name = ledger
listen = 127.0.0.1:9000
timeout = 45
retries = 3

[storage]
path = /var/lib/ledger
"""

failures = []
if pathlib.Path(TARGET).read_text() != EXPECTED:
    failures.append("service.ini does not hold exactly the new timeout")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    unread = peenaudit.read_before_write_violations(trial, [TARGET])
    if unread:
        failures.append(f"changed {TARGET} before reading it")
    results = trial.tool_results()
    refused = [
        call["name"]
        for call in trial.tool_calls()
        if call["name"] in peenaudit.MUTATING_TOOLS
        and results.get(call["callId"], {}).get("isError")
    ]
    if refused:
        failures.append(f"change tools were refused: {refused}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
