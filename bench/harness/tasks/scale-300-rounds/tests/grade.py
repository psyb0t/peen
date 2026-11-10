import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
import re

COUNT = 300
WORDS = ["amber", "birch", "cedar", "delta", "ember", "fjord", "garnet", "harbor", "indigo", "juniper", "kestrel", "lagoon", "marble", "nectar", "onyx", "pewter", "quartz", "russet", "saffron", "tundra", "umber", "violet", "willow", "xenon", "yarrow", "zephyr", "basalt", "cobalt", "dune", "falcon"]
LOOP = re.compile(r"\b(for|while|until|xargs)\b|-exec\b|\btr\b|\bcp\b")

wrong = []
for i in range(1, COUNT + 1):
    name = "f%03d.txt" % i
    word = "%s-%03d" % (WORDS[i % 30], i)
    source = pathlib.Path("/app/in", name)
    target = pathlib.Path("/app/out", name)
    if not source.is_file() or source.read_text() != word + "\n":
        wrong.append(f"input {name} changed")
    elif not target.is_file() or target.read_text() != word.upper() + "\n":
        wrong.append(name)
if wrong:
    failures.append(f"{len(wrong)} files are wrong or missing, first: {wrong[:5]}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    states = [row["state"] for row in trial.query("SELECT state FROM turns")]
    if states != ["completed"]:
        failures.append(f"turn states were {states}")
    loops = [
        call
        for call in trial.calls_named(peenaudit.TOOL_RUN_COMMAND)
        if LOOP.search(str(call["arguments"].get("command", "")))
    ]
    if loops:
        failures.append(f"{len(loops)} shell commands did bulk work against the rule")
    written = set()
    for call in trial.tool_calls():
        if call["name"] not in peenaudit.MUTATING_TOOLS:
            continue
        written.update(
            path for path in peenaudit.call_paths(call) if path.startswith("/app/out/")
        )
    if len(written) < COUNT:
        failures.append(f"the file tools wrote {len(written)} outputs, expected 300")
    print("INFO: rounds", len(trial.rounds()), "memory samples", len(trial.memory_samples()))

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
