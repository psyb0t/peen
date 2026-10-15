CREATE TABLE execution_inbox_messages (
    id TEXT PRIMARY KEY NOT NULL,
    session_id TEXT NOT NULL,
    execution_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    source_event_id TEXT,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    working_directory TEXT NOT NULL,
    message TEXT NOT NULL,
    parts_json TEXT NOT NULL CHECK (json_valid(parts_json) AND json_type(parts_json) = 'array'),
    state TEXT NOT NULL CHECK (state IN ('queued', 'delivered', 'deferred', 'forwarded')),
    delivered_activity_id TEXT,
    created_at DATETIME NOT NULL,
    delivered_at DATETIME,
    forwarded_execution_id TEXT,
    stop_revision INTEGER NOT NULL DEFAULT 0 CHECK (stop_revision >= 0),
    FOREIGN KEY (session_id, execution_id, turn_id) REFERENCES executions (session_id, id, root_turn_id),
    FOREIGN KEY (session_id, execution_id, delivered_activity_id) REFERENCES execution_activities (session_id, execution_id, id),
    FOREIGN KEY (session_id, forwarded_execution_id) REFERENCES executions (session_id, id),
    UNIQUE (session_id, request_id),
    UNIQUE (execution_id, sequence),
    UNIQUE (session_id, id),
    CHECK (state <> 'delivered' OR (delivered_activity_id IS NOT NULL AND delivered_at IS NOT NULL)),
    CHECK (state NOT IN ('queued', 'deferred', 'forwarded') OR (delivered_activity_id IS NULL AND delivered_at IS NULL)),
    CHECK ((state = 'forwarded') = (forwarded_execution_id IS NOT NULL))
);

CREATE INDEX execution_inbox_messages_execution_state_sequence_index
    ON execution_inbox_messages (execution_id, state, sequence ASC);
