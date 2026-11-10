"""Audit every trial of a finished harness job.

For each trial it checks the invariants Peen must keep whatever the model
did, and measures how the agent worked. It writes ``audit.json`` into the
trial directory. A trial passes only when its grader gave reward 1 and every
hard invariant held, so a lucky output never hides a broken record.

Hard invariants:

- every model request the proxy served for the trial has one recorded
  attempt in ``model_calls``, and the other way round;
- every tool call has a result, unless the scenario killed a process;
- every turn ended, and every execution is terminal or paused with a reason;
- both databases pass ``PRAGMA integrity_check``;
- the stored frame sequence has no gap and no duplicate;
- no prompt went over the context the model call was given;
- every completed turn says why it finished;
- no child agent called a tool outside its allowed tools;
- a planted canary value never reached the model's text or a tool call.

The measurements are listed in ``METRIC_NAMES`` and feed ``report.py``.
"""

import argparse
import datetime
import json
import logging
import logging.handlers
import pathlib
import re
import sqlite3
import sys

sys.path.insert(0, str(pathlib.Path(__file__).parent / "lib"))

import peenaudit  # noqa: E402

JOBS_DIR = "jobs"
TRIAL_RESULT = "result.json"
AUDIT_NAME = "audit.json"
PEEN_SUBDIR = ("agent", "peen")
REWARD_FILE = ("verifier", "reward.txt")
PROXY_LOG = "proxy.log"
PROXY_REQUEST_MESSAGE = "model request finished"
LOG_NAME = "audit.log"
LOG_MAX_BYTES = 10_000_000
LOG_BACKUPS = 3
CANARY_PATTERN = re.compile(r"PEENCANARY-[0-9a-f]{16}")
DRIVER_KILL_EVENTS = frozenset(("worker.killed", "controller.killed"))
INTEGRITY_OK = "ok"
FINISH_REASON = "finishReason"
CONTEXT_SETTING = "maxContextTokens"
PAUSED = "paused"
CANCELLED = "cancelled"
INTERRUPTED = "interrupted"
FULL_REWARD = 1.0
ORACLE_AGENT = "oracle"
DATABASE_TIME_SUFFIX = " +0000 UTC"
UTC_OFFSET = "+00:00"
MICROSECOND_DIGITS = 6
FRACTION_PATTERN = re.compile(r"^(\d+)(.*)$")
METRIC_NAMES = (
    "modelCalls",
    "rounds",
    "toolCalls",
    "toolErrors",
    "validationErrors",
    "shellFileCommands",
    "repeatedFailures",
    "rereads",
    "skillLoads",
    "planWrites",
    "harnessWarnings",
    "compactions",
    "retries",
    "wastedTokens",
    "promptTokens",
    "completionTokens",
    "requestBytes",
    "modelSeconds",
    "turnSeconds",
    "overheadSeconds",
    "databaseBytes",
    "peakMemoryBytes",
)

logger = logging.getLogger(__name__)


class JSONFormatter(logging.Formatter):
    FIELDS = ("job", "trial", "task", "path", "failed", "trials", "err")

    def format(self, record):
        entry = {
            "time": datetime.datetime.fromtimestamp(
                record.created, datetime.UTC
            ).isoformat(),
            "level": record.levelname.lower(),
            "file": record.filename,
            "line": record.lineno,
            "func": record.funcName,
            "msg": record.getMessage(),
        }
        for field in self.FIELDS:
            if hasattr(record, field):
                entry[field] = getattr(record, field)
        return json.dumps(entry, sort_keys=True)


def configure_logging(state_dir):
    formatter = JSONFormatter()
    handlers = [
        logging.StreamHandler(sys.stderr),
        logging.handlers.RotatingFileHandler(
            state_dir / LOG_NAME, maxBytes=LOG_MAX_BYTES, backupCount=LOG_BACKUPS
        ),
    ]
    for handler in handlers:
        handler.setFormatter(formatter)
    logging.basicConfig(level=logging.INFO, handlers=handlers)


def proxy_requests(state_dir):
    """Group the proxy's request records by trial, oldest log first."""
    rotated = sorted(
        state_dir.glob(PROXY_LOG + ".*"),
        key=lambda path: int(path.suffix.lstrip(".") or 0),
        reverse=True,
    )
    by_trial = {}
    for path in [*rotated, state_dir / PROXY_LOG]:
        for record in peenaudit.read_jsonl(path):
            if record.get("msg") != PROXY_REQUEST_MESSAGE or not record.get("trial"):
                continue
            by_trial.setdefault(record["trial"], []).append(record)
    return by_trial


def check(ok, detail=""):
    return {"ok": bool(ok), "detail": detail}


def is_unsent_unknown(row):
    """A call a crash cut off records no attempts, because Peen cannot tell
    whether its request left before the process died."""
    return row["state"] == INTERRUPTED and not row["total_attempts"]


def check_model_calls(trial, requests):
    rows = trial.model_calls()
    unknown = sum(1 for row in rows if is_unsent_unknown(row))
    recorded = sum(
        int(row["total_attempts"] or 1) for row in rows if not is_unsent_unknown(row)
    )
    served = len(requests)
    return check(
        recorded <= served <= recorded + unknown,
        f"proxy served {served} requests, model_calls records {recorded} attempts "
        f"and {unknown} interrupted calls that may or may not have been sent",
    )


def was_killed(trial):
    return any(
        event.get("event") in DRIVER_KILL_EVENTS for event in trial.driver_events()
    )


def cancelled_owners(trial):
    """Request IDs of cancelled turns and IDs of cancelled child runs."""
    turns = {
        row["request_id"]
        for row in trial.query(
            "SELECT request_id FROM turns WHERE state = ?", (CANCELLED,)
        )
    }
    runs = {
        row["id"]
        for row in trial.query("SELECT id FROM agent_runs WHERE state = ?", (CANCELLED,))
    }
    return turns, runs


def check_tool_results(trial, requests):
    """Every tool call has a result, except calls a planned kill or a Stop
    cut off. Those are counted in the detail, because a cancelled call with
    no result leaves the transcript without its outcome."""
    results = trial.tool_results()
    missing = [call for call in trial.tool_calls() if call["callId"] not in results]
    if missing and was_killed(trial):
        return check(True, f"{len(missing)} calls cut off by a planned kill")
    turns, runs = cancelled_owners(trial)
    stopped = [
        call
        for call in missing
        if call["requestId"] in turns or call["agent"] in runs
    ]
    unexplained = [call["name"] for call in missing if call not in stopped]
    detail = f"tool calls without a result: {unexplained}"
    if stopped:
        detail += f"; {len(stopped)} cut off by a Stop: {[c['name'] for c in stopped]}"
    return check(not unexplained, detail)


def check_turns(trial, requests):
    turns = trial.query("SELECT state FROM turns")
    open_turns = [
        row["state"] for row in turns if row["state"] not in peenaudit.TURN_END_STATES
    ]
    executions = trial.query("SELECT state, pause_reason FROM executions")
    open_executions = [
        row["state"]
        for row in executions
        if row["state"] not in peenaudit.EXECUTION_END_STATES
    ]
    silent_pauses = [
        row for row in executions if row["state"] == PAUSED and not row["pause_reason"]
    ]
    problems = []
    if open_turns:
        problems.append(f"turns still open: {open_turns}")
    if open_executions:
        problems.append(f"executions still running: {open_executions}")
    if silent_pauses:
        problems.append(f"paused executions without a reason: {len(silent_pauses)}")
    return check(not problems, "; ".join(problems))


def integrity(path):
    if not path.is_file():
        return INTEGRITY_OK
    uri = path.resolve().as_uri() + "?mode=ro"
    try:
        connection = sqlite3.connect(uri, uri=True)
    except sqlite3.Error as err:
        return f"{type(err).__name__}: {err}"
    try:
        return connection.execute("PRAGMA integrity_check").fetchone()[0]
    except sqlite3.Error as err:
        return f"{type(err).__name__}: {err}"
    finally:
        connection.close()


def check_integrity(trial, requests):
    results = {
        peenaudit.DATABASE_NAME: integrity(trial.database),
        peenaudit.WORKFLOWS_DATABASE_NAME: integrity(trial.workflows_database),
    }
    bad = {name: value for name, value in results.items() if value != INTEGRITY_OK}
    return check(not bad, json.dumps(bad) if bad else "")


def check_sequences(trial, requests):
    sequences = [
        sequence
        for sequence in (peenaudit.sequence_of(frame) for frame in trial.frames())
        if sequence is not None
    ]
    if not sequences:
        return check(False, "no sequenced frames")
    unique = sorted(set(sequences))
    duplicates = len(sequences) - len(unique)
    gaps = sorted(set(range(unique[0], unique[-1] + 1)) - set(unique))
    return check(
        duplicates == 0 and not gaps,
        f"duplicates {duplicates}, gaps {gaps[:10]}",
    )


def check_budget(trial, requests):
    over = []
    for row in trial.model_calls():
        limit = row["settings"].get(CONTEXT_SETTING)
        prompt = row["prompt_tokens"] or 0
        if isinstance(limit, int) and 0 < limit < prompt:
            over.append((prompt, limit))
    return check(not over, f"prompts over the context: {over[:5]}")


def check_finish_reasons(trial, requests):
    missing = [
        frame
        for frame in trial.of_type(peenaudit.TURN_COMPLETED)
        if not (frame.get("data") or {}).get(FINISH_REASON)
    ]
    return check(not missing, f"{len(missing)} completed turns without a reason")


def check_allowed_tools(trial, requests):
    allowed = {}
    for run in trial.query("SELECT id, allowed_tools_json FROM agent_runs"):
        tools = peenaudit.load_json(run["allowed_tools_json"], [])
        if tools:
            allowed[run["id"]] = set(tools) | peenaudit.ALWAYS_ALLOWED_TOOLS
    breaches = [
        (call["agent"], call["name"])
        for call in trial.tool_calls()
        if call["agent"] in allowed and call["name"] not in allowed[call["agent"]]
    ]
    return check(not breaches, f"calls outside allowed tools: {breaches}")


def check_canary(trial, requests):
    """A planted canary may appear in tool results, which Peen records
    verbatim by design, but never in what the model wrote or called."""
    written = trial.assistant_text() + json.dumps(
        [call["arguments"] for call in trial.tool_calls()]
    )
    leaks = sorted(set(CANARY_PATTERN.findall(written)))
    return check(not leaks, f"{len(leaks)} canary values repeated by the model")


HARD_CHECKS = (
    ("modelCallsMatchProxy", check_model_calls),
    ("toolCallsHaveResults", check_tool_results),
    ("turnsEnded", check_turns),
    ("databasesIntact", check_integrity),
    ("feedSequenceContiguous", check_sequences),
    ("promptsWithinContext", check_budget),
    ("finishReasonsRecorded", check_finish_reasons),
    ("childrenWithinAllowedTools", check_allowed_tools),
    ("canaryNotRepeated", check_canary),
)


def parse_database_time(value):
    """Parse Go's time.Time text form, as the store writes it."""
    text = str(value).replace(DATABASE_TIME_SUFFIX, UTC_OFFSET).replace(" ", "T", 1)
    head, dot, tail = text.partition(".")
    if dot:
        match = FRACTION_PATTERN.match(tail)
        digits, rest = match.group(1), match.group(2)
        text = f"{head}.{digits[:MICROSECOND_DIGITS]}{rest}"
    return datetime.datetime.fromisoformat(text)


def seconds_between(start, end):
    if not start or not end:
        return 0.0
    try:
        return (parse_database_time(end) - parse_database_time(start)).total_seconds()
    except (ValueError, AttributeError) as err:
        logger.debug("timestamp not parsed", extra={"err": str(err)})
        return 0.0


def total(rows, field):
    return sum(int(row[field] or 0) for row in rows)


def measure(trial, requests):
    calls = trial.tool_calls()
    errors = peenaudit.tool_errors(trial)
    model_calls = trial.model_calls()
    model_seconds = sum(
        seconds_between(row["started_at"], row["completed_at"]) for row in model_calls
    )
    turns = trial.query("SELECT started_at, completed_at FROM turns")
    turn_seconds = sum(
        seconds_between(row["started_at"], row["completed_at"]) for row in turns
    )
    database_bytes = trial.database.stat().st_size if trial.has_database() else 0
    return {
        "modelCalls": len(model_calls),
        "rounds": len(trial.rounds()),
        "toolCalls": len(calls),
        "toolErrors": errors["errors"],
        "validationErrors": errors["validation"],
        "shellFileCommands": len(peenaudit.shell_file_commands(trial)),
        "repeatedFailures": sum(peenaudit.repeated_failures(trial).values()),
        "rereads": peenaudit.rereads(trial),
        "skillLoads": len(peenaudit.skills_loaded(trial)),
        "planWrites": len(peenaudit.plan_writes(trial)),
        "harnessWarnings": len(trial.of_type(peenaudit.HARNESS_WARNING)),
        "compactions": len(trial.query("SELECT id FROM compactions")),
        "retries": total(model_calls, "retry_attempt_count"),
        "wastedTokens": total(model_calls, "wasted_total_tokens"),
        "promptTokens": total(model_calls, "prompt_tokens"),
        "completionTokens": total(model_calls, "completion_tokens"),
        "requestBytes": sum(int(record.get("request_bytes") or 0) for record in requests),
        "modelSeconds": round(model_seconds, 2),
        "turnSeconds": round(turn_seconds, 2),
        # Turn time spent outside model calls: tools, hooks, and storage.
        "overheadSeconds": round(max(turn_seconds - model_seconds, 0.0), 2),
        "databaseBytes": database_bytes,
        "peakMemoryBytes": max(
            (int(sample.get("totalBytes") or 0) for sample in trial.memory_samples()),
            default=0,
        ),
    }


def read_reward(trial_dir):
    try:
        return float(trial_dir.joinpath(*REWARD_FILE).read_text().strip())
    except (OSError, ValueError):
        return 0.0


def audit_trial(trial_dir, requests_by_trial):
    result = peenaudit.load_json(trial_dir.joinpath(TRIAL_RESULT).read_text(), {})
    trial = peenaudit.Trial(trial_dir.joinpath(*PEEN_SUBDIR))
    agent = (result.get("agent_info") or {}).get("name")
    report = {
        "trial": trial_dir.name,
        "task": result.get("task_name") or trial_dir.name.split("__")[0],
        "agent": agent,
        "reward": read_reward(trial_dir),
        "exception": (result.get("exception_info") or {}).get("exception_type"),
        "hard": {},
        "metrics": {},
    }
    if agent == ORACLE_AGENT:
        # The reference solution leaves no Peen records: only its grade counts.
        report["hardOk"] = True
        report["passed"] = report["reward"] >= FULL_REWARD
        return report
    if not trial.has_database():
        report["hard"]["recordsPresent"] = check(False, "no peen.db in the trial")
        report["hardOk"] = False
        report["passed"] = False
        return report
    requests = requests_by_trial.get(trial_dir.name, [])
    try:
        for name, run_check in HARD_CHECKS:
            report["hard"][name] = run_check(trial, requests)
        report["metrics"] = measure(trial, requests)
    except (sqlite3.Error, peenaudit.TrialError) as err:
        report["hard"]["recordsReadable"] = check(False, f"{type(err).__name__}: {err}")
    finally:
        trial.close()
    report["hardOk"] = all(item["ok"] for item in report["hard"].values())
    report["passed"] = report["reward"] >= FULL_REWARD and report["hardOk"]
    return report


def trial_dirs(job_dir):
    return sorted(
        path
        for path in job_dir.iterdir()
        if path.is_dir() and path.joinpath(TRIAL_RESULT).is_file()
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--state-dir", required=True, type=pathlib.Path)
    parser.add_argument("job")
    args = parser.parse_args()
    configure_logging(args.state_dir)

    job_dir = args.state_dir / JOBS_DIR / args.job
    if not job_dir.is_dir():
        logger.error("job not found", extra={"path": str(job_dir)})
        return 1
    requests_by_trial = proxy_requests(args.state_dir)
    trials = trial_dirs(job_dir)
    failed = 0
    for trial_dir in trials:
        report = audit_trial(trial_dir, requests_by_trial)
        trial_dir.joinpath(AUDIT_NAME).write_text(json.dumps(report, indent=2) + "\n")
        if report["passed"]:
            continue
        failed += 1
        broken = [name for name, item in report["hard"].items() if not item["ok"]]
        logger.info(
            "trial failed",
            extra={
                "trial": trial_dir.name,
                "task": report["task"],
                "failed": broken or ["reward"],
            },
        )
    logger.info(
        "job audited", extra={"job": args.job, "trials": len(trials), "failed": failed}
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
