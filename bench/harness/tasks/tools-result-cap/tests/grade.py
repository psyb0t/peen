import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

import collections
import hashlib
import json

SKU = "SK-" + hashlib.md5(b"sku4821").hexdigest()[:6].upper()

failures = []
path = pathlib.Path("/app/out/sku.txt")
if not path.is_file() or path.read_text().strip() != SKU:
    failures.append("/app/out/sku.txt does not hold the sku of row 4821")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    calls = trial.tool_calls(include_children=False)
    seen = collections.Counter(
        (call["name"], json.dumps(call["arguments"], sort_keys=True)) for call in calls
    )
    repeated = [name for (name, _), count in seen.items() if count > 1]
    if repeated:
        failures.append(f"tool calls were repeated unchanged: {repeated}")
    if len(calls) > 10:
        failures.append(f"{len(calls)} tool calls for a one-row lookup, at most 10 expected")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
