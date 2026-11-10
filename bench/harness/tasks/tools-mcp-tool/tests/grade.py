import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

MCP_TOOL = 'mcp__ledger__accountBalance'

failures = []
path = pathlib.Path("/app/out/balance.txt")
if not path.is_file() or path.read_text().strip() != "1337.50":
    failures.append("/app/out/balance.txt does not hold 1337.50")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    ready = [
        server
        for frame in trial.of_type("mcp.status")
        for server in (frame.get("data") or {}).get("servers") or []
        if server.get("name") == "ledger"
        and server.get("state") == "ready"
        and MCP_TOOL in (server.get("tools") or [])
    ]
    if not ready:
        failures.append("no mcp.status frame listed the ledger server as ready with its tool")
    results = trial.tool_results(include_children=False)
    calls = trial.calls_named(MCP_TOOL, include_children=False)
    good = [
        call
        for call in calls
        if call["arguments"].get("account") == "ACCT-204"
        and not results.get(call["callId"], {}).get("isError")
        and "1337.50" in results.get(call["callId"], {}).get("content", "")
    ]
    if not good:
        failures.append(f"{MCP_TOOL} was never called successfully for ACCT-204")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
