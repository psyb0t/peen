import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

EXPECTED = 'status: reviewed\nowner: ops\nitems: 12\nlate-entry: 7\n'
TARGET = "/app/report.txt"

failures = []
if pathlib.Path(TARGET).read_text() != EXPECTED:
    failures.append("report.txt is not the reviewed text with the late entry kept")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    results = trial.tool_results(include_children=False)
    calls = trial.tool_calls(include_children=False)
    refused_at = [
        index
        for index, call in enumerate(calls)
        if call["name"] in peenaudit.MUTATING_TOOLS
        and TARGET in peenaudit.call_paths(call)
        and results.get(call["callId"], {}).get("isError")
    ]
    if len(refused_at) > 2:
        failures.append(f"{len(refused_at)} changes of the report were refused, a retry loop")
    for index in refused_at:
        after = calls[index + 1 :]
        reread = next(
            (
                call
                for call in after
                if call["name"] == "readFile"
                and peenaudit.normalize_path(call["arguments"].get("path")) == TARGET
            ),
            None,
        )
        nxt = after[0] if after else None
        if reread is None:
            failures.append("a refused change was not followed by a readFile of the report")
        elif nxt is not None and nxt["name"] in peenaudit.MUTATING_TOOLS:
            failures.append("a refused change was retried before the file was read again")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
