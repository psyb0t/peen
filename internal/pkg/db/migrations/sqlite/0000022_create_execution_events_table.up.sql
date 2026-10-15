CREATE TABLE execution_events (
    id TEXT PRIMARY KEY NOT NULL,
    session_id TEXT NOT NULL,
    execution_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    event_type TEXT NOT NULL,
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    created_at DATETIME NOT NULL,
    FOREIGN KEY (session_id, execution_id) REFERENCES executions (session_id, id),
    UNIQUE (execution_id, sequence),
    UNIQUE (session_id, id)
);

CREATE INDEX execution_events_execution_sequence_id_index
    ON execution_events (execution_id, sequence ASC, id ASC);
