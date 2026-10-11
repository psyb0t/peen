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
