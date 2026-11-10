import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
path = pathlib.Path("/app/out/version.txt")
if not path.is_file() or path.read_text().strip() != "4.2.7":
    failures.append("/app/out/version.txt does not hold 4.2.7")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    failed = [
        server
        for frame in trial.of_type("mcp.status")
        for server in (frame.get("data") or {}).get("servers") or []
        if server.get("name") == "flaky" and server.get("state") == "failed"
    ]
    if not failed:
        failures.append("no mcp.status frame reported the flaky server as failed")
    elif not any(server.get("error") for server in failed):
        failures.append("the failed server carries no error text")
    if not trial.of_type(peenaudit.TURN_COMPLETED):
        failures.append("the turn did not complete")
    used = [
        call["name"]
        for call in trial.tool_calls(include_children=False)
        if call["name"].startswith("mcp__flaky__")
    ]
    if used:
        failures.append(f"a tool of the broken server was called: {used}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
