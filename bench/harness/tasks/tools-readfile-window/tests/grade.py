import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

import hashlib

TARGET = 17344
TOKEN = hashlib.md5(str(TARGET).encode()).hexdigest()[:8]
LOG = "/app/data/events.log"

failures = []
path = pathlib.Path("/app/out/token.txt")
if not path.is_file() or path.read_text().strip() != TOKEN:
    failures.append("/app/out/token.txt does not hold the token of seq=17344")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    reads = [
        call
        for call in trial.calls_named("readFile", include_children=False)
        if peenaudit.normalize_path(call["arguments"].get("path")) == LOG
    ]
    searches = [
        call
        for call in trial.calls_named("searchText", include_children=False)
        if str(TARGET) in str(call["arguments"].get("pattern", ""))
    ]
    covering = [
        call
        for call in reads
        if int(call["arguments"].get("offset") or 1)
        <= TARGET
        < int(call["arguments"].get("offset") or 1)
        + int(call["arguments"].get("limit") or 2000)
    ]
    if not covering and not searches:
        failures.append("no readFile window or search reached line 17344")
    from_start = [
        call for call in reads if int(call["arguments"].get("offset") or 1) <= 1
    ]
    if len(from_start) > 1:
        failures.append("read the start of the file more than once")
    if len(reads) > 4:
        failures.append(f"{len(reads)} readFile calls on the log, it was paged through")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
