import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
code = pathlib.Path("/app/out/code.txt")
if not code.is_file() or code.read_text() != "quill-5521\n":
    failures.append("/app/out/code.txt does not hold the code")
if pathlib.Path("/app/out/woke.txt").exists():
    failures.append("the handler woke the session despite delivery queue")

EVENT_TYPE = "deploy.note"

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    frames = trial.frames()
    kinds = [frame["type"] for frame in frames]
    starts = trial.of_type(peenaudit.TURN_STARTED)
    if len(starts) != 1:
        failures.append(f"expected one turn, found {len(starts)}")
    elif (starts[0].get("data") or {}).get("originEventType"):
        failures.append("the turn was started by the event")
    if "notice.created" not in kinds or "userMessage.created" not in kinds:
        failures.append("the notice or the user message was never recorded")
    elif kinds.index("notice.created") > kinds.index("userMessage.created"):
        failures.append("the event was not posted before the user message")
    delivered = [
        notice
        for frame in trial.of_type(peenaudit.SESSION_EVENTS)
        for notice in (frame.get("data") or {}).get("notices") or []
        if notice.get("type") == EVENT_TYPE
    ]
    if not delivered:
        failures.append("the event never arrived with the user's turn")
    elif delivered[0].get("delivery") != "wake":
        failures.append("the posted event did not say wake, so nothing was tested")
    handler_messages = trial.query(
        "SELECT id FROM messages WHERE content LIKE '%<handler>%'"
    )
    if handler_messages:
        failures.append("a handler message was sent to the model")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
