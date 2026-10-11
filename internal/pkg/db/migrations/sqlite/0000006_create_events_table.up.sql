CREATE TABLE events (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    request_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    parent_tool_call_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    worker_generation_id TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, turn_id) REFERENCES turns (session_id, id),
    UNIQUE (session_id, id),
    UNIQUE (session_id, sequence)
);

CREATE INDEX events_session_sequence_id_index ON events (session_id, sequence, id);

CREATE INDEX events_worker_generation_id_index
    ON events (worker_generation_id)
    WHERE worker_generation_id <> '';
