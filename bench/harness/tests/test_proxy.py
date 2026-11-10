import io
import json

import pytest

import proxy


def schedule(*faults, replay=None):
    data = {"faults": list(faults)}
    if replay is not None:
        data["replay"] = replay
    return proxy.parse_fault_file(data)


@pytest.mark.parametrize(
    "data",
    [
        [],
        {"faults": {}},
        {"faults": [{"action": "explode", "request": 1}]},
        {"faults": [{"action": "status", "status": 500}]},
        {"faults": [{"action": "status", "status": 500, "request": 1, "every": 2}]},
        {"faults": [{"action": "status", "status": 200, "request": 1}]},
        {"faults": [{"action": "status", "status": "500", "request": 1}]},
        {"faults": [{"action": "status", "status": 500, "request": True}]},
        {"faults": [{"action": "status", "status": 500, "request": 0}]},
        {"faults": [{"action": "status", "status": 500, "requests": []}]},
        {"faults": [{"action": "stall", "request": 1}]},
        {"faults": [{"action": "stall", "request": 1, "seconds": 99999}]},
        {"faults": [{"action": "truncate", "request": 1}]},
        {"faults": [{"action": "cutStream", "request": 1, "afterBytes": -1}]},
        {"faults": [{"action": "drop", "request": 1, "model": 5}]},
        {"replay": {"steps": []}},
        {"replay": {"steps": [{"toolCalls": [{"arguments": {}}]}]}},
        {"replay": {"steps": [{"toolCalls": [{"name": "x", "arguments": []}]}]}},
        {"replay": {"steps": [{"content": 5}]}},
    ],
)
def test_bad_fault_files_are_refused(data):
    with pytest.raises(proxy.FaultFileError):
        proxy.parse_fault_file(data)


def next_faults(trials, model, count, now=0.0):
    return [trials.next_request("trial", model, now)[1] for _ in range(count)]


def test_selectors_and_model_filters(tmp_path):
    trials = proxy.Trials(tmp_path)
    trials.states["trial"] = proxy.TrialState(
        schedule(
            {"action": "status", "status": 500, "request": 2},
            {"action": "drop", "requests": [4, 5]},
            {"action": "status", "status": 429, "every": 3, "model": "glm-5.3-flash"},
        )
    )
    actions = [
        (fault or {}).get("action") for fault in next_faults(trials, "glm-5.3", 5)
    ]
    assert actions == [None, "status", None, "drop", "drop"]
    flash = [fault for fault in next_faults(trials, "glm-5.3-flash", 3)]
    assert flash[:2] == [None, None]
    assert flash[2]["status"] == 429


def test_a_stage_filter_counts_only_its_own_requests(tmp_path):
    trials = proxy.Trials(tmp_path)
    trials.states["trial"] = proxy.TrialState(
        schedule(
            {"action": "status", "status": 500, "stage": "compaction", "request": 1},
            {"action": "drop", "stage": "turn", "model": "m", "request": 3},
        )
    )
    turn = [trials.next_request("trial", "m", 0.0)[1] for _ in range(2)]
    compaction = trials.next_request("trial", "m", 0.0, has_tools=False)[1]
    third_turn = trials.next_request("trial", "m", 0.0)[1]
    assert turn == [None, None]
    assert compaction["status"] == 500
    assert third_turn["action"] == "drop"


def test_an_unknown_stage_is_refused():
    with pytest.raises(proxy.FaultFileError):
        schedule({"action": "drop", "stage": "summary", "request": 1})


def test_an_outage_answers_every_request_until_it_ends(tmp_path):
    trials = proxy.Trials(tmp_path)
    trials.states["trial"] = proxy.TrialState(
        schedule({"action": "outage", "request": 1, "seconds": 30})
    )
    assert trials.next_request("trial", "m", 100.0)[1]["action"] == "outage"
    assert trials.next_request("trial", "m", 120.0)[1]["action"] == "outage"
    assert trials.next_request("trial", "m", 131.0)[1] is None


def test_a_trial_without_a_file_has_no_faults(tmp_path):
    trials = proxy.Trials(tmp_path)
    assert trials.next_request("nothing", "m", 0.0) == (1, None, None)


def test_the_fault_file_is_read_from_the_faults_directory(tmp_path):
    (tmp_path / "trial.json").write_text(
        json.dumps({"faults": [{"action": "drop", "request": 1}]})
    )
    trials = proxy.Trials(tmp_path)
    assert trials.next_request("trial", "m", 0.0)[1]["action"] == "drop"


def test_a_broken_fault_file_is_refused(tmp_path):
    (tmp_path / "trial.json").write_text("{not json")
    with pytest.raises(proxy.FaultFileError):
        proxy.Trials(tmp_path).next_request("trial", "m", 0.0)


def test_replay_steps_repeat_and_hold_the_last(tmp_path):
    trials = proxy.Trials(tmp_path)
    trials.states["trial"] = proxy.TrialState(
        schedule(
            replay={
                "steps": [
                    {
                        "repeat": 2,
                        "toolCalls": [
                            {"name": "readFile", "arguments": {"path": "/app/in/{i}.txt"}}
                        ],
                    },
                    {"content": "done after {i}"},
                ]
            }
        )
    )
    steps = [trials.next_request("trial", "m", 0.0)[2] for _ in range(4)]
    first = proxy.replay_reply("m", steps[0], 50)
    call = first["delta"]["tool_calls"][0]
    assert first["finish"] == proxy.FINISH_TOOL_CALLS
    assert json.loads(call["function"]["arguments"]) == {"path": "/app/in/0.txt"}
    assert json.loads(proxy.replay_reply("m", steps[1], 50)["delta"]["tool_calls"][0]
                      ["function"]["arguments"]) == {"path": "/app/in/1.txt"}
    last = proxy.replay_reply("m", steps[3], 50)
    assert last["delta"]["content"] == "done after 3"
    assert last["finish"] == proxy.FINISH_STOP
    assert last["usage"]["prompt_tokens"] == 50


class FakeResponse:
    def __init__(self, body, chunk=7):
        self.data = body
        self.chunk = chunk

    def read1(self, size):
        piece, self.data = self.data[: self.chunk], self.data[self.chunk :]
        return piece


def sse(*events):
    return b"".join(
        b"data: " + json.dumps(event).encode() + b"\n\n" for event in events
    ) + b"data: [DONE]\n\n"


def chunk(content=None, arguments=None, finish=None, usage=None):
    delta = {}
    if content is not None:
        delta["content"] = content
    if arguments is not None:
        delta["tool_calls"] = [
            {"index": 0, "id": "c", "function": {"name": "x", "arguments": arguments}}
        ]
    event = {"id": "r", "model": "m", "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}
    if usage:
        event["usage"] = usage
    return event


def relay(body, fault):
    handler = proxy.Handler.__new__(proxy.Handler)
    handler.wfile = io.BytesIO()
    handler.close_connection = False
    fields = {"response_bytes": 0}
    handler.relay_stream(FakeResponse(body), fields, fault)
    return handler, fields


def written_events(handler):
    return [
        proxy.parse_sse_event(raw)
        for raw in handler.wfile.getvalue().split(proxy.SSE_EVENT_END)
        if raw
    ]


STREAM = sse(
    chunk(content="hel"),
    chunk(content="lo"),
    chunk(arguments='{"path":'),
    chunk(arguments='"/app"}'),
    chunk(finish="tool_calls", usage={"prompt_tokens": 9, "completion_tokens": 4}),
)


def test_a_clean_stream_passes_through_and_records_usage():
    handler, fields = relay(STREAM, None)
    assert handler.wfile.getvalue() == STREAM
    assert fields["prompt_tokens"] == 9
    assert fields["finish_reason"] == "tool_calls"
    assert fields["response_bytes"] == len(STREAM)


def test_truncate_ends_the_stream_with_a_length_finish():
    handler, fields = relay(STREAM, {"action": "truncate", "afterEvents": 2})
    events = written_events(handler)
    assert [event["choices"][0]["delta"].get("content") for event in events[:2]] == ["hel", "lo"]
    assert events[2]["choices"][0]["finish_reason"] == "length"
    assert events[3] is None
    assert fields["finish_reason"] == "length"


def test_corrupt_tool_arguments_breaks_the_joined_json():
    handler, _ = relay(STREAM, {"action": "corruptToolArguments"})
    events = [event for event in written_events(handler) if event]
    arguments = "".join(
        call["function"]["arguments"]
        for event in events
        for call in event["choices"][0]["delta"].get("tool_calls", [])
    )
    assert arguments.startswith(proxy.CORRUPT_ARGUMENTS_PREFIX)
    with pytest.raises(ValueError):
        json.loads(arguments)


def test_cut_stream_stops_at_the_byte_limit():
    handler, fields = relay(STREAM, {"action": "cutStream", "afterBytes": 60})
    assert len(handler.wfile.getvalue()) == 60
    assert fields["response_bytes"] == 60
    assert handler.close_connection
