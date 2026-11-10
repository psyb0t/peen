import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
import os

WORDS = {"0": "fjord-7", "1": "garnet-18", "2": "harbor-29", "3": "indigo-40", "4": "juniper-51", "5": "kestrel-62", "6": "lagoon-73", "7": "marble-84", "8": "nectar-95", "9": "onyx-106"}
NAMES = ["main", "s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9"]

for key, word in WORDS.items():
    tree = pathlib.Path(f"/app/w{key}")
    out = tree / "out.txt"
    if not out.is_file() or out.read_text() != word.upper() + "\n":
        failures.append(f"/app/w{key}/out.txt does not hold the capitalized input")
    extra = sorted(set(os.listdir(tree)) - {"input.txt", "out.txt", ".plans"})
    if extra:
        failures.append(f"/app/w{key} holds unexpected entries: {extra}")
stray = sorted(set(os.listdir("/app")) - {f"w{key}" for key in range(10)})
if stray:
    failures.append(f"/app holds unexpected entries: {stray}")


def foreign_paths(frames, own):
    """Paths a session's tool calls named outside its own tree."""
    tree = f"/app/w{own}"
    found = []
    for frame in frames:
        if frame["type"] != peenaudit.TOOL_USE:
            continue
        data = frame.get("data") or {}
        arguments = data.get("arguments") or {}
        if data.get("name") == peenaudit.TOOL_RUN_COMMAND:
            command = str(arguments.get("command", ""))
            if any(f"/app/w{m}" in command for m in range(10) if m != own):
                found.append(command)
            continue
        call = {"name": data.get("name"), "arguments": arguments}
        for path in peenaudit.call_paths(call, tree):
            if path and path != tree and not path.startswith(tree + "/"):
                found.append(path)
    return found


if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    states = [row["state"] for row in trial.query("SELECT state FROM turns")]
    if states != ["completed"] * 10:
        failures.append(f"turn states were {states}")
    for key, name in enumerate(NAMES):
        frames = trial.frames() if name == "main" else trial.session_frames(name)
        if not frames:
            failures.append(f"session {name} left no frames")
        foreign = foreign_paths(frames, key)
        if foreign:
            failures.append(f"session {name} touched outside /app/w{key}: {foreign[:3]}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
