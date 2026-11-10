import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
path = pathlib.Path("/app/out/reference.txt")
if not path.is_file() or path.read_text().strip() != "INV-7342-QK":
    failures.append("/app/out/reference.txt does not hold INV-7342-QK")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    results = trial.tool_results(include_children=False)
    reads = [
        call
        for call in trial.calls_named("readFile", include_children=False)
        if str(call["arguments"].get("path", "")).endswith("invoice.pdf")
    ]
    if not reads:
        failures.append("readFile never read invoice.pdf")
    elif all(results.get(call["callId"], {}).get("isError") for call in reads):
        failures.append("every readFile of invoice.pdf failed")
    shell = [
        call
        for call in trial.calls_named("runCommand", include_children=False)
        if "invoice.pdf" in str(call["arguments"].get("command", ""))
    ]
    if shell:
        failures.append("a shell command worked on invoice.pdf")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
