import pytest

import peenaudit
import trialbuilder as tb


def trial_of(build_trial, *frames):
    builder = build_trial().add_turn().add_frames(*frames)
    return peenaudit.Trial(builder.write() / "agent" / "peen")


def test_frames_skip_log_lines_and_read_child_calls(build_trial):
    trial = trial_of(
        build_trial,
        tb.tool_use(1, "a", "readFile", {"path": "notes.txt"}),
        tb.tool_result(2, "a", "readFile"),
        tb.child_tool_use(3, "run-1", "b", "listFiles"),
    )
    calls = trial.tool_calls()
    assert [call["name"] for call in calls] == ["readFile", "listFiles"]
    assert calls[0]["agent"] is None
    assert calls[1]["agent"] == "run-1"
    assert len(trial.tool_calls(include_children=False)) == 1
    assert len(trial.frames()) == 3


PATCH = "*** Begin Patch\n*** Update File: main.go\n@@\n-a\n+b\n*** End Patch"


@pytest.mark.parametrize(
    ("frames", "violated"),
    [
        (
            [
                tb.tool_use(1, "a", "readFile", {"path": "/app/main.go"}),
                tb.tool_use(2, "b", "editFile", {"path": "main.go", "edits": []}),
            ],
            False,
        ),
        (
            [
                tb.tool_use(1, "a", "editFile", {"path": "/app/main.go"}),
                tb.tool_use(2, "b", "readFile", {"path": "/app/main.go"}),
            ],
            True,
        ),
        ([tb.tool_use(1, "a", "applyPatch", {"patch": PATCH})], True),
        (
            [
                tb.tool_use(
                    1,
                    "a",
                    "movePath",
                    {"source": "/app/main.go", "destination": "/app/b.go"},
                )
            ],
            True,
        ),
        ([tb.tool_use(1, "a", "writeFile", {"path": "/app/other.go"})], False),
    ],
)
def test_read_before_write(build_trial, frames, violated):
    trial = trial_of(build_trial, *frames)
    violations = peenaudit.read_before_write_violations(trial, ["/app/main.go"])
    assert ("/app/main.go" in violations) is violated


@pytest.mark.parametrize(
    ("command", "flagged"),
    [
        ("cat /app/x.txt", True),
        ("go test ./...", False),
        ("cd /app && grep -r foo .", True),
        ("sed -i s/a/b/ f", True),
        ("sed s/a/b/ f > g", False),
        ("ls", True),
        ("echo $(find . -name x)", True),
        ("make lint", False),
    ],
)
def test_shell_file_commands(build_trial, command, flagged):
    trial = trial_of(build_trial, tb.tool_use(1, "a", "runCommand", {"command": command}))
    assert bool(peenaudit.shell_file_commands(trial)) is flagged


def test_repeated_failures_count_identical_failing_commands(build_trial):
    frames = []
    for index in range(3):
        call = f"call-{index}"
        frames.append(tb.tool_use(index * 2 + 1, call, "runCommand", {"command": "make x"}))
        frames.append(tb.tool_result(index * 2 + 2, call, "runCommand", '{"exitCode":2}'))
    frames.append(tb.tool_use(7, "ok", "runCommand", {"command": "make y"}))
    frames.append(tb.tool_result(8, "ok", "runCommand", '{"exitCode":0}'))
    frames.append(tb.tool_use(9, "err", "runCommand", {"command": "make y"}))
    frames.append(tb.tool_result(10, "err", "runCommand", "boom", is_error=True))
    trial = trial_of(build_trial, *frames)
    assert peenaudit.repeated_failures(trial) == {"make x": 3}
    assert peenaudit.repeated_failures(trial, threshold=1) == {"make x": 3, "make y": 1}


def test_rereads_reset_after_a_change(build_trial):
    trial = trial_of(
        build_trial,
        tb.tool_use(1, "a", "readFile", {"path": "/app/f"}),
        tb.tool_use(2, "b", "readFile", {"path": "/app/f"}),
        tb.tool_use(3, "c", "writeFile", {"path": "/app/f"}),
        tb.tool_use(4, "d", "readFile", {"path": "/app/f"}),
        tb.tool_use(5, "e", "readFile", {"path": "/app/f", "offset": 40}),
    )
    assert peenaudit.rereads(trial) == 1


def test_rounds_come_from_the_database(build_trial):
    builder = build_trial().add_turn()
    builder.add_model_call(["changeDirectory"])
    builder.add_model_call(["readFile", "readFile"])
    builder.add_model_call(["listFiles"], stage="child", agent_run_id="run-1")
    builder.add_model_call(stage="compaction")
    trial = peenaudit.Trial(builder.write() / "agent" / "peen")
    assert peenaudit.rounds_with(trial, "changeDirectory") == [["changeDirectory"]]
    assert len(trial.rounds()) == 2
    assert len(trial.rounds("run-1")) == 1
    assert len(trial.model_calls()) == 4


def test_a_trial_without_a_database_says_so(tmp_path):
    trial = peenaudit.Trial(tmp_path)
    assert trial.model_calls() == []
    with pytest.raises(peenaudit.TrialError):
        trial.query("SELECT 1")
