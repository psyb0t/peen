CREATE TABLE model_runs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    agent_run_id TEXT,
    stage TEXT NOT NULL CHECK (stage IN ('turn', 'child', 'compaction')),
    model_reference TEXT NOT NULL,
    connection_name TEXT NOT NULL,
    requested_model_id TEXT NOT NULL,
    response_model_id TEXT NOT NULL DEFAULT '',
    request_settings_json TEXT NOT NULL CHECK (json_valid(request_settings_json)),
    state TEXT NOT NULL CHECK (state IN ('running', 'completed', 'failed', 'cancelled', 'interrupted')),
    response_text TEXT NOT NULL DEFAULT '',
    response_thinking TEXT NOT NULL DEFAULT '',
    response_messages_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(response_messages_json)),
    response_injections_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(response_injections_json)),
    response_usage_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(response_usage_json)),
    response_cost_amount TEXT NOT NULL DEFAULT '',
    retry_cost_amount TEXT NOT NULL DEFAULT '',
    billed_cost_amount TEXT NOT NULL DEFAULT '',
    cost_known BOOLEAN NOT NULL DEFAULT FALSE,
    finish_reason TEXT NOT NULL DEFAULT '',
    failure_classification TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL,
    completed_at DATETIME,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, turn_id) REFERENCES turns (session_id, id),
    FOREIGN KEY (session_id, agent_run_id) REFERENCES agent_runs (session_id, id),
    UNIQUE (session_id, id)
);

CREATE INDEX model_runs_session_started_at_id_index
    ON model_runs (session_id, started_at DESC, id DESC);
CREATE INDEX model_runs_turn_started_at_id_index
    ON model_runs (turn_id, started_at ASC, id ASC);
CREATE INDEX model_runs_agent_run_started_at_id_index
    ON model_runs (agent_run_id, started_at ASC, id ASC);

CREATE TABLE model_calls (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    model_run_id TEXT NOT NULL,
    round INTEGER NOT NULL CHECK (round >= 0),
    request_messages_json TEXT NOT NULL CHECK (json_valid(request_messages_json)),
    request_tools_json TEXT NOT NULL CHECK (json_valid(request_tools_json)),
    state TEXT NOT NULL CHECK (state IN ('running', 'completed', 'failed', 'cancelled', 'interrupted')),
    response_message_json TEXT NOT NULL DEFAULT 'null' CHECK (json_valid(response_message_json)),
    response_usage_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(response_usage_json)),
    retry_attempts_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(retry_attempts_json)),
    retry_attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_attempt_count >= 0),
    prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
    completion_tokens INTEGER NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
    total_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
    reasoning_tokens INTEGER NOT NULL DEFAULT 0 CHECK (reasoning_tokens >= 0),
    cache_read_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0),
    cache_write_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),
    cache_write_long_ttl_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_long_ttl_tokens >= 0),
    wasted_prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK (wasted_prompt_tokens >= 0),
    wasted_completion_tokens INTEGER NOT NULL DEFAULT 0 CHECK (wasted_completion_tokens >= 0),
    wasted_total_tokens INTEGER NOT NULL DEFAULT 0 CHECK (wasted_total_tokens >= 0),
    total_attempts INTEGER NOT NULL DEFAULT 0 CHECK (total_attempts >= 0),
    response_model_id TEXT NOT NULL DEFAULT '',
    finish_reason TEXT NOT NULL DEFAULT '',
    response_cost_amount TEXT NOT NULL DEFAULT '',
    retry_cost_amount TEXT NOT NULL DEFAULT '',
    billed_cost_amount TEXT NOT NULL DEFAULT '',
    cost_known BOOLEAN NOT NULL DEFAULT FALSE,
    failure_classification TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL,
    completed_at DATETIME,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, model_run_id) REFERENCES model_runs (session_id, id),
    UNIQUE (session_id, id),
    UNIQUE (model_run_id, round)
);

CREATE INDEX model_calls_run_round_id_index
    ON model_calls (model_run_id, round ASC, id ASC);
