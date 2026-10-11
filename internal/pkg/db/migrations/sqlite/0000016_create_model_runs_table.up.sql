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
