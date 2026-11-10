import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
for name, text in (("tern.txt", "seen\n"), ("done.txt", "done\n")):
    path = pathlib.Path("/app/out", name)
    if not path.is_file() or path.read_text() != text:
        failures.append(f"/app/out/{name} does not hold {text.strip()}")

EVENT_TYPE = "note.ready"

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    frames = trial.frames()
    kinds = [frame["type"] for frame in frames]
    if kinds.count(peenaudit.TURN_STARTED) != 1:
        failures.append("the event started a turn of its own or the turn never ran")
    delivered = [
        index
        for index, frame in enumerate(frames)
        if frame["type"] == peenaudit.SESSION_EVENTS
        and any(
            notice.get("type") == EVENT_TYPE
            for notice in (frame.get("data") or {}).get("notices") or []
        )
    ]
    ends = [index for index, kind in enumerate(kinds) if kind in peenaudit.TURN_ENDS]
    if not delivered:
        failures.append("the event never reached the model in a session.events frame")
    elif not ends or delivered[0] > ends[0]:
        failures.append("the event arrived only after the turn ended")
    elif not any(
        kind == peenaudit.TOOL_USE for kind in kinds[delivered[0] + 1 : ends[0]]
    ):
        failures.append("the turn did not go on working after the event was delivered")
    elif peenaudit.TOOL_RESULT not in kinds[: delivered[0]]:
        failures.append("the event was delivered before any tool boundary")
    if "notice.created" not in kinds:
        failures.append("the event was never recorded as a notice")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
