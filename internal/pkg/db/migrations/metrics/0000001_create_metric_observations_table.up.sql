CREATE TABLE metric_observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    observed_at_ms INTEGER NOT NULL,
    kind TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    worker_generation_id TEXT NOT NULL DEFAULT '',
    turn_id TEXT NOT NULL DEFAULT '',
    agent_run_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    stage TEXT NOT NULL DEFAULT '',
    function_name TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    first_token_ms INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    retries INTEGER NOT NULL DEFAULT 0,
    count INTEGER NOT NULL DEFAULT 0,
    bytes INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX metric_observations_observed_at_index
    ON metric_observations (observed_at_ms);

CREATE INDEX metric_observations_session_index
    ON metric_observations (session_id, observed_at_ms);

CREATE INDEX metric_observations_kind_index
    ON metric_observations (kind, observed_at_ms);
