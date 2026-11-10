import json

import pytest

import audit
import compare
import report
import trialbuilder as tb
from conftest import TRIAL_NAME

CANARY = "PEENCANARY-0123456789abcdef"


def run_audit(builder, proxy_count=2):
    trial_dir = builder.write()
    requests = {TRIAL_NAME: tb.proxy_records(TRIAL_NAME, proxy_count)}
    return audit.audit_trial(trial_dir, requests)


def failed_checks(result):
    return sorted(name for name, item in result["hard"].items() if not item["ok"])


def test_a_clean_trial_passes(clean_trial):
    result = run_audit(clean_trial)
    assert failed_checks(result) == []
    assert result["passed"]
    assert result["metrics"]["toolCalls"] == 1
    assert result["metrics"]["modelCalls"] == 2
    assert result["metrics"]["modelSeconds"] == pytest.approx(9.0)
    assert result["metrics"]["turnSeconds"] == pytest.approx(10.0)
    assert result["metrics"]["overheadSeconds"] == pytest.approx(1.0)
    assert result["metrics"]["requestBytes"] == 20


def test_a_reward_below_one_fails_a_clean_record(build_trial):
    builder = build_trial(reward=0.0).add_turn()
    builder.add_model_call()
    builder.add_frames(tb.frame(1, "turn.completed", {"text": "x", "finishReason": "stop"}))
    result = run_audit(builder, proxy_count=1)
    assert result["hardOk"]
    assert not result["passed"]


@pytest.mark.parametrize("proxy_count", [1, 3])
def test_proxy_and_model_calls_must_agree(clean_trial, proxy_count):
    result = run_audit(clean_trial, proxy_count=proxy_count)
    assert failed_checks(result) == ["modelCallsMatchProxy"]


def test_retried_attempts_count_against_the_proxy(build_trial):
    builder = build_trial().add_turn()
    builder.add_model_call(attempts=3)
    builder.add_frames(tb.frame(1, "turn.completed", {"text": "x", "finishReason": "stop"}))
    assert run_audit(builder, proxy_count=3)["hard"]["modelCallsMatchProxy"]["ok"]


@pytest.mark.parametrize(("proxy_count", "ok"), [(0, False), (1, True), (2, True), (3, False)])
def test_an_interrupted_call_may_or_may_not_have_reached_the_proxy(
    build_trial, proxy_count, ok
):
    builder = build_trial().add_turn()
    builder.add_model_call()
    builder.add_model_call(attempts=0, state=audit.INTERRUPTED)
    builder.add_frames(tb.frame(1, "turn.completed", {"text": "x", "finishReason": "stop"}))
    result = run_audit(builder, proxy_count=proxy_count)
    assert result["hard"]["modelCallsMatchProxy"]["ok"] is ok


@pytest.mark.parametrize(("killed", "ok"), [(False, False), (True, True)])
def test_a_missing_tool_result_is_allowed_only_after_a_kill(clean_trial, killed, ok):
    clean_trial.add_frames(tb.tool_use(5, "lost", "runCommand", {"command": "x"}))
    if killed:
        clean_trial.add_driver_event("worker.killed")
    assert run_audit(clean_trial)["hard"]["toolCallsHaveResults"]["ok"] is ok


def test_calls_cut_off_by_a_stop_are_excused_and_counted(clean_trial):
    clean_trial.add_turn(state="cancelled", turn_id="turn-2", request_id="stopped")
    clean_trial.add_frames(
        tb.frame(5, "tool.use", {"callId": "s", "name": "runCommand", "arguments": {}},
                 request_id="stopped"),
    )
    result = run_audit(clean_trial)
    assert result["hard"]["toolCallsHaveResults"]["ok"]
    assert "1 cut off by a Stop" in result["hard"]["toolCallsHaveResults"]["detail"]


def test_a_cancelled_child_call_is_excused(clean_trial):
    clean_trial.add_agent_run("run-1", [])
    clean_trial.connection.execute("UPDATE agent_runs SET state = 'cancelled'")
    clean_trial.add_frames(tb.child_tool_use(5, "run-1", "c1", "runCommand"))
    assert run_audit(clean_trial)["hard"]["toolCallsHaveResults"]["ok"]


def test_a_sequence_gap_fails(clean_trial):
    clean_trial.add_frames(tb.frame(9, "content_block_stop", {}))
    assert failed_checks(run_audit(clean_trial)) == ["feedSequenceContiguous"]


def test_a_duplicate_sequence_fails(clean_trial):
    clean_trial.add_frames(tb.frame(4, "content_block_stop", {}))
    assert failed_checks(run_audit(clean_trial)) == ["feedSequenceContiguous"]


def test_an_open_turn_fails(clean_trial):
    clean_trial.add_turn(state="running", turn_id="turn-2")
    assert failed_checks(run_audit(clean_trial)) == ["turnsEnded"]


def test_a_prompt_over_the_context_fails(clean_trial):
    clean_trial.add_model_call(prompt_tokens=5000, context=1000)
    assert failed_checks(run_audit(clean_trial, proxy_count=3)) == ["promptsWithinContext"]


def test_a_child_tool_outside_its_allowed_tools_fails(clean_trial):
    clean_trial.add_agent_run("run-1", ["readFile"])
    clean_trial.add_frames(
        tb.child_tool_use(5, "run-1", "c1", "runCommand"),
        tb.child_tool_result(6, "run-1", "c1", "runCommand"),
        tb.child_tool_use(7, "run-1", "c2", "ask"),
        tb.child_tool_result(8, "run-1", "c2", "ask"),
    )
    result = run_audit(clean_trial)
    detail = result["hard"]["childrenWithinAllowedTools"]["detail"]
    assert failed_checks(result) == ["childrenWithinAllowedTools"]
    assert "runCommand" in detail
    assert "'ask'" not in detail


@pytest.mark.parametrize(
    ("frame", "ok"),
    [
        (tb.tool_result(5, "a", "readFile", f"TOKEN={CANARY}"), True),
        (tb.frame(5, "turn.completed", {"text": CANARY, "finishReason": "stop"}), False),
        (tb.tool_use(5, "b", "runCommand", {"command": f"echo {CANARY}"}), False),
    ],
)
def test_the_canary_may_be_read_but_never_repeated(clean_trial, frame, ok):
    clean_trial.add_frames(frame, tb.tool_result(6, "b", "runCommand"))
    assert run_audit(clean_trial)["hard"]["canaryNotRepeated"]["ok"] is ok


def test_a_completed_turn_without_a_reason_fails(clean_trial):
    clean_trial.add_frames(tb.frame(5, "turn.completed", {"text": "x"}))
    assert failed_checks(run_audit(clean_trial)) == ["finishReasonsRecorded"]


def test_a_trial_without_a_database_fails(build_trial):
    builder = build_trial()
    builder.write()
    builder.database.unlink()
    result = audit.audit_trial(builder.trial_dir, {})
    assert failed_checks(result) == ["recordsPresent"]


@pytest.mark.parametrize(
    ("start", "end", "seconds"),
    [
        (
            "2026-11-04 04:10:10.287692893 +0000 UTC",
            "2026-11-04 04:10:14.770246914 +0000 UTC",
            4.482554,
        ),
        ("2026-11-04 04:10:10 +0000 UTC", "2026-11-04 04:10:12 +0000 UTC", 2.0),
        (None, "2026-11-04 04:10:12 +0000 UTC", 0.0),
        ("not a time", "2026-11-04 04:10:12 +0000 UTC", 0.0),
    ],
)
def test_seconds_between_reads_go_time_text(start, end, seconds):
    assert audit.seconds_between(start, end) == pytest.approx(seconds, abs=1e-5)


def write_job(build_trial, root, name, rewards):
    job_dir = root / "jobs" / name
    for index, reward in enumerate(rewards):
        trial = f"tools-sample__t{index}"
        builder = build_trial(trial, reward, root=job_dir).add_turn()
        builder.add_model_call()
        builder.add_frames(tb.frame(1, "turn.completed", {"text": "x", "finishReason": "stop"}))
        result = audit.audit_trial(builder.write(), {trial: tb.proxy_records(trial, 1)})
        (job_dir / trial / "audit.json").write_text(json.dumps(result))
    return job_dir


def test_report_and_compare(build_trial, tmp_path):
    first_dir = write_job(build_trial, tmp_path, "first", [1.0] * 10)
    second_dir = write_job(build_trial, tmp_path, "second", [1.0] * 2 + [0.0] * 8)

    first = report.build_report("first", first_dir)
    assert first["tasks"]["tools-sample"]["passes"] == 10
    assert set(first["groups"]) == {"tools"}
    assert "| tools-sample | 10 of 10 |" in report.render_markdown(first)

    second = report.build_report("second", second_dir)
    comparison = compare.build_comparison("first", "second", first, second)
    assert comparison["tasks"]["tools-sample"]["distinct"]
    assert comparison["suite"]["difference"] == pytest.approx(-0.8)
    assert "-80 points" in compare.render_markdown(comparison)


def test_compare_lists_moved_measurements():
    first = {"metrics": {"toolCalls": {"median": 10}, "rereads": {"median": 0}}}
    second = {"metrics": {"toolCalls": {"median": 20}, "rereads": {"median": 2}}}
    changes = compare.metric_changes(first, second)
    assert changes["toolCalls"]["change"] == pytest.approx(1.0)
    assert changes["rereads"]["change"] is None
