"""Read what one Peen trial recorded and check how the agent behaved.

Graders import this inside the task container, where the adapter uploads it
to ``/peen/lib``, and ``audit.py`` imports it over finished trials on the
host. It uses only the standard library and runs on Python 3.11, the oldest
interpreter in the task images.

A trial directory is Peen's ``/peen`` directory: ``frames.jsonl``, the
``state`` directory with ``peen.db``, and, for a scenario trial,
``driver.jsonl``. Everything here reads; nothing writes.
"""

import json
import os
import pathlib
import posixpath
import re
import sqlite3

FRAMES_NAME = "frames.jsonl"
DRIVER_LOG_NAME = "driver.jsonl"
MEMORY_LOG_NAME = "memory.jsonl"
STATE_DIR_NAME = "state"
DATABASE_NAME = "peen.db"
WORKFLOWS_DATABASE_NAME = "workflows.db"
DEFAULT_WORKSPACE = "/app"

TOOL_USE = "tool.use"
TOOL_RESULT = "tool.result"
CHILD_TOOL_USE = "agent.run.tool.use"
CHILD_TOOL_RESULT = "agent.run.tool.result"
TURN_STARTED = "turn.started"
TURN_COMPLETED = "turn.completed"
TURN_FAILED = "turn.failed"
TURN_CANCELLED = "turn.cancelled"
TURN_ENDS = (TURN_COMPLETED, TURN_FAILED, TURN_CANCELLED)
HARNESS_WARNING = "harness.warning"
PROVIDER_RETRY = "provider.retry"
SKILL_INJECTED = "skill.injected"
SESSION_EVENTS = "session.events"
AGENT_RUN_STARTED = "agent.run.started"
QUESTION_ASKED = "question.asked"
CONTENT_DELTA = "content_block_delta"
USER_MESSAGE_CREATED = "userMessage.created"

TOOL_READ_FILE = "readFile"
TOOL_WRITE_FILE = "writeFile"
TOOL_EDIT_FILE = "editFile"
TOOL_APPLY_PATCH = "applyPatch"
TOOL_MOVE_PATH = "movePath"
TOOL_REMOVE_PATH = "removePath"
TOOL_RUN_COMMAND = "runCommand"
TOOL_USE_SKILL = "useSkill"
TOOL_CHANGE_DIRECTORY = "changeDirectory"
TOOL_LAUNCH_AGENT = "launchAgent"
MUTATING_TOOLS = frozenset(
    (
        TOOL_WRITE_FILE,
        TOOL_EDIT_FILE,
        TOOL_APPLY_PATCH,
        TOOL_MOVE_PATH,
        TOOL_REMOVE_PATH,
    )
)
# Every agent has these whatever its allowed-tools says.
ALWAYS_ALLOWED_TOOLS = frozenset(("sendMessage", "waitMessages", "stopAgent", "ask"))
EXECUTION_END_STATES = frozenset(("completed", "failed", "cancelled", "paused"))
TURN_END_STATES = frozenset(("completed", "failed", "cancelled", "interrupted"))
STAGE_TURN = "turn"
STAGE_CHILD = "child"
STAGE_COMPACTION = "compaction"
PATCH_FILE_LINE = re.compile(r"^\*\*\* (?:Add|Update|Delete) File: (.+)$", re.M)
PATCH_MOVE_LINE = re.compile(r"^\*\*\* Move to: (.+)$", re.M)
# A shell command doing what a file tool does: reading, listing, searching,
# or editing files in place.
SHELL_FILE_COMMAND = re.compile(
    r"(?:^|[;&|]\s*|\$\(\s*)"
    r"(?:cat|head|tail|less|ls|find|tree|grep|rg|egrep|fgrep|sed\s+-i|awk|wc\s+-l)"
    r"(?:\s|$)"
)
VALIDATION_MARKERS = ("invalid argument", "validation", "required", "must be", "unknown field")


class TrialError(Exception):
    """The trial directory is missing a record the check needs."""


def read_jsonl(path):
    """Return every JSON object in a file of one object per line.

    Lines that are not JSON objects, such as a log line a process printed to
    the same stream, are skipped.
    """
    records = []
    try:
        handle = open(path, encoding="utf-8", errors="replace")
    except FileNotFoundError:
        return records
    with handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            try:
                record = json.loads(line)
            except ValueError:
                continue
            if isinstance(record, dict):
                records.append(record)
    return records


def is_frame(record):
    return "type" in record and "data" in record and "level" not in record


def sequence_of(frame):
    metadata = frame.get("metadata") or {}
    sequence = metadata.get("sequence")
    return sequence if isinstance(sequence, int) else None


def normalize_path(path, cwd=DEFAULT_WORKSPACE):
    if not isinstance(path, str) or not path:
        return ""
    if not path.startswith("/"):
        path = posixpath.join(cwd, path)
    return posixpath.normpath(path)


def patch_paths(patch, cwd=DEFAULT_WORKSPACE):
    if not isinstance(patch, str):
        return []
    found = PATCH_FILE_LINE.findall(patch) + PATCH_MOVE_LINE.findall(patch)
    return [normalize_path(path.strip(), cwd) for path in found]


def call_paths(call, cwd=DEFAULT_WORKSPACE):
    """Return the paths a file tool call names, normalized to absolute."""
    arguments = call["arguments"]
    name = call["name"]
    if name == TOOL_APPLY_PATCH:
        return patch_paths(arguments.get("patch") or arguments.get("input"), cwd)
    if name == TOOL_MOVE_PATH:
        return [
            normalize_path(arguments.get("source") or arguments.get("from"), cwd),
            normalize_path(arguments.get("destination") or arguments.get("to"), cwd),
        ]
    return [normalize_path(arguments.get("path"), cwd)]


class Trial:
    """The records of one trial, read lazily from its Peen directory."""

    def __init__(self, peen_dir):
        self.dir = pathlib.Path(peen_dir)
        self._frames = None
        self._connection = None

    @property
    def database(self):
        return self.dir / STATE_DIR_NAME / DATABASE_NAME

    @property
    def workflows_database(self):
        return self.dir / STATE_DIR_NAME / WORKFLOWS_DATABASE_NAME

    def has_database(self):
        return self.database.is_file()

    def frames(self):
        if self._frames is None:
            self._frames = [
                record
                for record in read_jsonl(self.dir / FRAMES_NAME)
                if is_frame(record)
            ]
        return self._frames

    def session_frames(self, name):
        return [
            record
            for record in read_jsonl(self.dir / f"frames-{name}.jsonl")
            if is_frame(record)
        ]

    def driver_events(self):
        return read_jsonl(self.dir / DRIVER_LOG_NAME)

    def memory_samples(self):
        return read_jsonl(self.dir / MEMORY_LOG_NAME)

    def of_type(self, *types):
        return [frame for frame in self.frames() if frame["type"] in types]

    def connect(self):
        if self._connection is None:
            if not self.has_database():
                raise TrialError(f"{self.database} is missing")
            uri = self.database.resolve().as_uri() + "?mode=ro"
            self._connection = sqlite3.connect(uri, uri=True)
            self._connection.row_factory = sqlite3.Row
        return self._connection

    def query(self, sql, parameters=()):
        return [dict(row) for row in self.connect().execute(sql, parameters)]

    def close(self):
        if self._connection is not None:
            self._connection.close()
            self._connection = None

    # Tool calls, from the frame stream. Root calls carry agent None.

    def tool_calls(self, include_children=True):
        calls = []
        for index, frame in enumerate(self.frames()):
            data = frame.get("data") or {}
            if frame["type"] == TOOL_USE:
                event, agent = data, None
            elif include_children and frame["type"] == CHILD_TOOL_USE:
                event, agent = data.get("event") or {}, data.get("agentRunId")
            else:
                continue
            calls.append(
                {
                    "index": index,
                    "sequence": sequence_of(frame),
                    "agent": agent,
                    "callId": event.get("callId"),
                    "name": event.get("name"),
                    "arguments": event.get("arguments") or {},
                    "requestId": (frame.get("metadata") or {}).get("requestId"),
                }
            )
        return calls

    def tool_results(self, include_children=True):
        results = {}
        for index, frame in enumerate(self.frames()):
            data = frame.get("data") or {}
            if frame["type"] == TOOL_RESULT:
                event, agent = data, None
            elif include_children and frame["type"] == CHILD_TOOL_RESULT:
                event, agent = data.get("event") or {}, data.get("agentRunId")
            else:
                continue
            results[event.get("callId")] = {
                "index": index,
                "agent": agent,
                "name": event.get("name"),
                "content": event.get("content") or "",
                "isError": bool(event.get("isError")),
            }
        return results

    def calls_named(self, name, include_children=True):
        return [
            call
            for call in self.tool_calls(include_children)
            if call["name"] == name
        ]

    # Model rounds, from the database: one row per model call.

    def model_calls(self):
        if not self.has_database():
            return []
        rows = self.query(
            """
            SELECT mc.id, mc.state, mc.prompt_tokens, mc.completion_tokens,
                   mc.total_tokens, mc.retry_attempt_count, mc.total_attempts,
                   mc.wasted_total_tokens, mc.finish_reason,
                   mc.failure_classification, mc.billed_cost_amount,
                   mc.cost_known, mc.response_message_json,
                   mc.started_at, mc.completed_at,
                   mr.stage, mr.agent_run_id, mr.turn_id,
                   mr.request_settings_json, mr.model_reference
            FROM model_calls mc
            JOIN model_runs mr ON mr.id = mc.model_run_id
            ORDER BY mc.started_at
            """
        )
        for row in rows:
            row["toolCalls"] = response_tool_calls(row.pop("response_message_json"))
            row["settings"] = load_json(row.pop("request_settings_json"), {})
        return rows

    def rounds(self, agent=None):
        """Model calls of one agent's turn rounds: the root's for None, a
        child's for its agent run ID. Compaction calls are left out."""
        stage = STAGE_TURN if agent is None else STAGE_CHILD
        return [
            row
            for row in self.model_calls()
            if row["stage"] == stage and row["agent_run_id"] == agent
        ]

    def final_text(self):
        ends = self.of_type(TURN_COMPLETED)
        if not ends:
            return ""
        return (ends[-1].get("data") or {}).get("text") or ""

    def assistant_text(self):
        """Every piece of text the model produced: deltas, replies, and
        child agent responses."""
        parts = []
        for frame in self.frames():
            data = frame.get("data") or {}
            if frame["type"] == CONTENT_DELTA:
                delta = data.get("delta") or {}
                parts.append(delta.get("text") or "")
            elif frame["type"] == TURN_COMPLETED:
                parts.append(data.get("text") or "")
        if self.has_database():
            for row in self.query("SELECT response_text FROM agent_runs"):
                parts.append(row["response_text"] or "")
        return "\n".join(parts)


def load_json(raw, default):
    if not raw:
        return default
    try:
        return json.loads(raw)
    except ValueError:
        return default


def response_tool_calls(raw):
    message = load_json(raw, {})
    calls = []
    for call in (message or {}).get("ToolCalls") or []:
        calls.append(
            {"name": call.get("Name"), "arguments": call.get("Arguments") or {}}
        )
    return calls


# Behavioral checks. Each returns the offending calls or a count, so a grader
# can both fail and say why.


def mutations_before_read(trial, path, include_children=False):
    """Calls that changed path before any readFile of it in the trial."""
    target = normalize_path(path)
    offenders = []
    has_read = False
    for call in trial.tool_calls(include_children):
        paths = call_paths(call) if call["name"] in MUTATING_TOOLS else []
        if call["name"] == TOOL_READ_FILE and target in call_paths(call):
            has_read = True
        elif target in paths and not has_read:
            offenders.append(call)
    return offenders


def read_before_write_violations(trial, existing_paths, include_children=False):
    """Map each pre-existing path to the calls that changed it unread."""
    violations = {}
    for path in existing_paths:
        offenders = mutations_before_read(trial, path, include_children)
        if offenders:
            violations[path] = offenders
    return violations


def shell_file_commands(trial, include_children=True):
    """runCommand calls that read, list, search, or edit files a file tool
    covers."""
    return [
        call
        for call in trial.calls_named(TOOL_RUN_COMMAND, include_children)
        if SHELL_FILE_COMMAND.search(str(call["arguments"].get("command", "")))
    ]


def repeated_failures(trial, threshold=3, include_children=True):
    """Commands that failed with identical text threshold times or more."""
    results = trial.tool_results(include_children)
    counts = {}
    for call in trial.calls_named(TOOL_RUN_COMMAND, include_children):
        result = results.get(call["callId"])
        if result is None or not command_failed(result):
            continue
        command = str(call["arguments"].get("command", "")).strip()
        counts[command] = counts.get(command, 0) + 1
    return {command: count for command, count in counts.items() if count >= threshold}


EXIT_CODE_PATTERN = re.compile(r'"exitCode"\s*:\s*(-?\d+)')


def command_failed(result):
    if result["isError"]:
        return True
    match = EXIT_CODE_PATTERN.search(result["content"])
    return bool(match) and int(match.group(1)) != 0


def rereads(trial, include_children=True):
    """readFile calls repeating an earlier read of the same window of a file
    nothing changed in between."""
    seen = {}
    count = 0
    for call in trial.tool_calls(include_children):
        key_agent = call["agent"]
        if call["name"] in MUTATING_TOOLS or call["name"] == TOOL_RUN_COMMAND:
            seen = {key: value for key, value in seen.items() if key[0] != key_agent}
            continue
        if call["name"] != TOOL_READ_FILE:
            continue
        arguments = call["arguments"]
        key = (
            key_agent,
            normalize_path(arguments.get("path")),
            arguments.get("offset"),
            arguments.get("limit"),
        )
        if key in seen:
            count += 1
        seen[key] = True
    return count


def tool_errors(trial, include_children=True):
    results = trial.tool_results(include_children)
    errors = [result for result in results.values() if result["isError"]]
    validation = [
        result
        for result in errors
        if any(marker in result["content"].lower() for marker in VALIDATION_MARKERS)
    ]
    return {"errors": len(errors), "validation": len(validation), "calls": len(results)}


def skills_loaded(trial):
    """Skill names the agent loaded with useSkill or the turn injected."""
    names = [
        str(call["arguments"].get("name", ""))
        for call in trial.calls_named(TOOL_USE_SKILL)
    ]
    names += [
        str((frame.get("data") or {}).get("name", ""))
        for frame in trial.of_type(SKILL_INJECTED)
    ]
    return names


def rounds_with(trial, tool_name, agent=None):
    """Root model rounds that called tool_name, with every tool they called."""
    return [
        [call["name"] for call in row["toolCalls"]]
        for row in trial.rounds(agent)
        if any(call["name"] == tool_name for call in row["toolCalls"])
    ]


def plan_writes(trial):
    return [
        call
        for call in trial.tool_calls()
        if call["name"] in MUTATING_TOOLS
        and any("/.plans/" in path for path in call_paths(call))
    ]


def file_contains(path, needle):
    try:
        return needle in pathlib.Path(path).read_text(errors="replace")
    except OSError:
        return False


def walk_files(root, skip=(".git",)):
    for directory, names, files in os.walk(root):
        names[:] = [name for name in names if name not in skip]
        for name in files:
            yield pathlib.Path(directory) / name


def has_frames():
    """True when the grader runs after Peen, not after the oracle."""
    return pathlib.Path("/peen", FRAMES_NAME).is_file()
