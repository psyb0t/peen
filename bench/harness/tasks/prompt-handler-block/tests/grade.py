import json
import pathlib
import re
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
EVENT_TYPE = "ci.build.failed"
for name, word in (("start.txt", "ready"), ("ci-report.txt", "handled")):
    path = pathlib.Path("/app", name)
    if not path.is_file() or path.read_text().strip() != word:
        failures.append(f"/app/{name} does not hold {word}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    frames = trial.frames()
    handler_frames = [
        (index, frame)
        for index, frame in enumerate(frames)
        if frame["type"] == peenaudit.USER_MESSAGE_CREATED
        and str((frame.get("data") or {}).get("message", "")).lstrip().startswith("<handler>")
    ]
    if not handler_frames:
        failures.append("no turn started with a handler block")
    else:
        handler_index, handler_frame = handler_frames[0]
        text = handler_frame["data"]["message"]
        if f"<event>{EVENT_TYPE}</event>" not in text or "ci-report.txt" not in text:
            failures.append("the handler block does not carry the event type and body")
        notices = [
            (frame.get("data") or {})
            for frame in frames
            if frame["type"] == "notice.created"
            and (frame.get("data") or {}).get("type") == EVENT_TYPE
        ]
        started = [
            (frame.get("data") or {})
            for frame in trial.of_type(peenaudit.TURN_STARTED)
            if (frame.get("data") or {}).get("originEventType") == EVENT_TYPE
        ]
        if not started:
            failures.append("no turn started with the event as its origin")
        elif not any(
            start.get("originEventId") == notice.get("id")
            for start in started
            for notice in notices
        ):
            failures.append("the woken turn's origin is not the posted event")
        delivered = [
            index
            for index, frame in enumerate(frames)
            if index > handler_index
            and frame["type"] == peenaudit.SESSION_EVENTS
            and any(
                notice.get("type") == EVENT_TYPE
                for notice in (frame.get("data") or {}).get("notices", [])
            )
        ]
        if not delivered:
            failures.append("the event was not delivered after the handler block")
        reports = [
            call
            for call in trial.tool_calls(include_children=False)
            if "ci-report" in json.dumps(call["arguments"])
        ]
        if reports and reports[0]["index"] < handler_index:
            failures.append("the report was written before the handler block arrived")
        if not reports:
            failures.append("no tool call wrote the report")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
