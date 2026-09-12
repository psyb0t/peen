CREATE TABLE agent_runs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    parent_turn_id TEXT NOT NULL,
    parent_agent_run_id TEXT,
    parent_tool_call_id TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL,
    name TEXT NOT NULL,
    definition TEXT NOT NULL CHECK (definition IN ('stored', 'ad-hoc')),
    depth INTEGER NOT NULL CHECK (depth > 0),
    workspace TEXT NOT NULL,
    model_reference TEXT NOT NULL,
    model_id TEXT NOT NULL,
    task TEXT NOT NULL,
    instructions TEXT NOT NULL,
    allowed_tools_json TEXT NOT NULL CHECK (json_valid(allowed_tools_json)),
    system_prompt TEXT NOT NULL,
    event_count INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0),
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    state TEXT NOT NULL CHECK (state IN ('running', 'completed', 'failed', 'cancelled', 'interrupted')),
    response_text TEXT NOT NULL DEFAULT '',
    response_thinking TEXT NOT NULL DEFAULT '',
    response_messages_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(response_messages_json)),
    finish_reason TEXT NOT NULL DEFAULT '',
    prompt_token_count INTEGER NOT NULL DEFAULT 0 CHECK (prompt_token_count >= 0),
    completion_token_count INTEGER NOT NULL DEFAULT 0 CHECK (completion_token_count >= 0),
    failure_classification TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL,
    ended_at DATETIME,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, parent_turn_id) REFERENCES turns (session_id, id),
    FOREIGN KEY (session_id, parent_agent_run_id) REFERENCES agent_runs (session_id, id),
    UNIQUE (session_id, id)
);

CREATE INDEX agent_runs_session_started_at_id_index
    ON agent_runs (session_id, started_at DESC, id DESC);
CREATE INDEX agent_runs_session_state_started_at_id_index
    ON agent_runs (session_id, state, started_at DESC, id DESC);
CREATE INDEX agent_runs_parent_agent_run_id_index
    ON agent_runs (parent_agent_run_id, started_at ASC, id ASC);

CREATE TABLE agent_run_events (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    agent_run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    event_type TEXT NOT NULL,
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    created_at DATETIME NOT NULL,
    FOREIGN KEY (session_id, agent_run_id) REFERENCES agent_runs (session_id, id),
    UNIQUE (session_id, id),
    UNIQUE (agent_run_id, sequence)
);

CREATE INDEX agent_run_events_run_sequence_id_index
    ON agent_run_events (agent_run_id, sequence ASC, id ASC);
