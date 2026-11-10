import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
path = pathlib.Path("/app/out/number.txt")
if not path.is_file() or path.read_text().strip() != "38164":
    failures.append("/app/out/number.txt does not hold 38164")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    results = trial.tool_results(include_children=False)
    reads = [
        call
        for call in trial.calls_named("readFile", include_children=False)
        if str(call["arguments"].get("path", "")).endswith("badge.png")
    ]
    # A model without image input gets a clear refusal from readFile, and
    # then any way to the number is fine. A model with image input must read
    # the image with readFile.
    refusals = [
        results.get(call["callId"], {})
        for call in reads
        if results.get(call["callId"], {}).get("isError")
    ]
    is_text_only = any("does not accept images" in r["content"] for r in refusals)
    if not reads:
        failures.append("readFile never read badge.png")
    elif len(refusals) == len(reads) and not is_text_only:
        failures.append("every readFile of badge.png failed")
    shell = [
        call
        for call in trial.calls_named("runCommand", include_children=False)
        if "badge.png" in str(call["arguments"].get("command", ""))
    ]
    if shell and not is_text_only:
        failures.append("a shell command worked on badge.png")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
