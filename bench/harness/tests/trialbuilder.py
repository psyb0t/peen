"""Build trial directories for the harness tool tests.

The database is created from Peen's real migrations, so a schema change
that breaks a query here breaks these tests too. ``BENCH_MIGRATIONS_DIR``
names the migrations directory; without it the checkout's copy is used.
"""

import json
import os
import pathlib
import sqlite3
import uuid

HARNESS_DIR = pathlib.Path(__file__).resolve().parent.parent
ENV_MIGRATIONS_DIR = "BENCH_MIGRATIONS_DIR"
CHECKOUT_MIGRATIONS = (
    HARNESS_DIR.parent.parent / "internal" / "pkg" / "db" / "migrations" / "sqlite"
)
TIME = "2026-11-04 04:10:10.000000000 +0000 UTC"
LATER = "2026-11-04 04:10:14.500000000 +0000 UTC"
LATEST = "2026-11-04 04:10:20.000000000 +0000 UTC"


def migrations_dir():
    return pathlib.Path(os.environ.get(ENV_MIGRATIONS_DIR) or CHECKOUT_MIGRATIONS)


def frame(sequence, frame_type, data, request_id="request-1"):
    return {
        "id": str(uuid.uuid4()),
        "type": frame_type,
        "data": data,
        "metadata": {
            "sequence": sequence,
            "sessionId": "session-1",
            "requestId": request_id,
        },
    }


def tool_use(sequence, call_id, name, arguments):
    return frame(
        sequence,
        "tool.use",
        {"callId": call_id, "name": name, "arguments": arguments},
    )


def tool_result(sequence, call_id, name, content="{}", is_error=False):
    return frame(
        sequence,
        "tool.result",
        {"callId": call_id, "name": name, "content": content, "isError": is_error},
    )


def child_tool_use(sequence, agent_run_id, call_id, name):
    return frame(
        sequence,
        "agent.run.tool.use",
        {
            "agentRunId": agent_run_id,
            "parentTurnId": "turn-1",
            "event": {"callId": call_id, "name": name, "arguments": {}},
        },
    )


def child_tool_result(sequence, agent_run_id, call_id, name):
    return frame(
        sequence,
        "agent.run.tool.result",
        {
            "agentRunId": agent_run_id,
            "parentTurnId": "turn-1",
            "event": {
                "callId": call_id,
                "name": name,
                "content": "{}",
                "isError": False,
            },
        },
    )


class TrialBuilder:
    """A trial directory under root, written as the adapter downloads it."""

    def __init__(self, root, name="tools-sample__abc123", reward=1.0):
        self.trial_dir = pathlib.Path(root) / name
        self.peen_dir = self.trial_dir / "agent" / "peen"
        (self.peen_dir / "state").mkdir(parents=True)
        (self.trial_dir / "verifier").mkdir(parents=True)
        (self.trial_dir / "verifier" / "reward.txt").write_text(f"{reward}\n")
        (self.trial_dir / "result.json").write_text(
            json.dumps({"task_name": name.split("__")[0], "trial_name": name})
        )
        self.frames = []
        self.database = self.peen_dir / "state" / "peen.db"
        self.connection = sqlite3.connect(self.database)
        self.connection.execute("PRAGMA foreign_keys = OFF")
        for path in sorted(migrations_dir().glob("*.up.sql")):
            self.connection.executescript(path.read_text())
        self.connection.execute(
            "INSERT INTO sessions (id, created_at, updated_at, root_agent, model_id) "
            "VALUES ('session-1', ?, ?, 'core:general-purpose', 'zai/glm-5.3')",
            (TIME, TIME),
        )

    def add_frames(self, *frames):
        self.frames.extend(frames)
        return self

    def add_turn(self, state="completed", turn_id="turn-1", request_id=None):
        self.connection.execute(
            "INSERT INTO turns (id, session_id, request_id, workspace, state, "
            "started_at, completed_at) VALUES (?, 'session-1', ?, '/app', ?, ?, ?)",
            (turn_id, request_id or turn_id + "-request", state, TIME, LATEST),
        )
        return self

    def add_model_call(
        self,
        tool_calls=(),
        prompt_tokens=100,
        attempts=1,
        context=1000,
        stage="turn",
        agent_run_id=None,
        state="completed",
    ):
        run_id = str(uuid.uuid4())
        settings = json.dumps({"maxContextTokens": context})
        self.connection.execute(
            "INSERT INTO model_runs (id, session_id, turn_id, agent_run_id, stage, "
            "model_reference, connection_name, requested_model_id, "
            "request_settings_json, state, started_at) VALUES "
            "(?, 'session-1', 'turn-1', ?, ?, 'zai/glm-5.3', 'zai', 'glm-5.3', ?, "
            "'completed', ?)",
            (run_id, agent_run_id, stage, settings, TIME),
        )
        message = {
            "Role": "assistant",
            "ToolCalls": [
                {"ID": f"call-{index}", "Name": name, "Arguments": {}}
                for index, name in enumerate(tool_calls)
            ],
        }
        self.connection.execute(
            "INSERT INTO model_calls (id, session_id, model_run_id, round, "
            "request_messages_json, request_tools_json, state, "
            "response_message_json, prompt_tokens, total_attempts, started_at, "
            "completed_at) VALUES (?, 'session-1', ?, 0, '[]', '[]', ?, "
            "?, ?, ?, ?, ?)",
            (
                str(uuid.uuid4()),
                run_id,
                state,
                json.dumps(message),
                prompt_tokens,
                attempts,
                TIME,
                LATER,
            ),
        )
        return self

    def add_agent_run(self, agent_run_id, allowed_tools):
        self.connection.execute(
            "INSERT INTO agent_runs (id, session_id, parent_turn_id, request_id, "
            "name, definition, depth, workspace, model_reference, model_id, task, "
            "instructions, allowed_tools_json, system_prompt, state, started_at) "
            "VALUES (?, 'session-1', 'turn-1', 'request-1', 'helper', 'stored', 1, "
            "'/app', 'zai/glm-5.3', 'glm-5.3', 'task', '', ?, '', 'completed', ?)",
            (agent_run_id, json.dumps(allowed_tools), TIME),
        )
        return self

    def add_driver_event(self, event):
        with open(self.peen_dir / "driver.jsonl", "a") as handle:
            handle.write(json.dumps({"event": event}) + "\n")
        return self

    def write(self):
        self.connection.commit()
        self.connection.close()
        with open(self.peen_dir / "frames.jsonl", "w") as handle:
            handle.write('{"level":"DEBUG","msg":"a log line on stdout"}\n')
            for item in self.frames:
                handle.write(json.dumps(item) + "\n")
        return self.trial_dir


def proxy_records(trial, count):
    return [
        {"msg": "model request finished", "trial": trial, "request_bytes": 10}
        for _ in range(count)
    ]
