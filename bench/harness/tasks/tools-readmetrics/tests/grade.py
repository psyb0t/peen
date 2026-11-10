import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
path = pathlib.Path("/app/out/count.txt")
text = path.read_text().strip() if path.is_file() else ""
if not text.isdigit():
    failures.append("/app/out/count.txt does not hold a whole number")

if peenaudit and peenaudit.has_frames() and text.isdigit():
    trial = peenaudit.Trial("/peen")
    calls = trial.tool_calls(include_children=False)
    results = trial.tool_results(include_children=False)
    reads = [
        index
        for index, call in enumerate(calls)
        if call["name"] == "readMetrics"
        and not results.get(call["callId"], {}).get("isError")
    ]
    if not reads:
        failures.append("readMetrics was never called successfully")
    elif int(text) not in reads:
        # A call is recorded when it finishes, so the report holds exactly the
        # calls that came before readMetrics, the readMetrics call excluded.
        failures.append(
            f"count.txt holds {text}, the calls before readMetrics were {reads}"
        )
    summaries = [calls[index]["arguments"].get("summary") for index in reads]
    if reads and "tools" not in summaries:
        failures.append("readMetrics was not asked for the tools summary")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
