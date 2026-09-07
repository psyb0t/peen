CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    last_message_at DATETIME,
    message_count INTEGER NOT NULL DEFAULT 0 CHECK (message_count >= 0),
    completed_turn_count INTEGER NOT NULL DEFAULT 0 CHECK (completed_turn_count >= 0),
    last_completed_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_completed_sequence >= 0),
    active_context_hash TEXT NOT NULL DEFAULT '',
    root_agent TEXT NOT NULL,
    model_id TEXT NOT NULL
);

CREATE TABLE context_snapshots (
    hash TEXT PRIMARY KEY,
    manifest_json TEXT NOT NULL CHECK (json_valid(manifest_json)),
    resolved_content TEXT NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE TABLE prompt_snapshots (
    hash TEXT PRIMARY KEY,
    effective_prompt TEXT NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE TABLE turns (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    workspace TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('running', 'completed', 'failed', 'cancelled', 'interrupted')),
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    started_at DATETIME NOT NULL,
    completed_at DATETIME,
    failure_classification TEXT NOT NULL DEFAULT '',
    context_snapshot_hash TEXT,
    prompt_snapshot_hash TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (context_snapshot_hash) REFERENCES context_snapshots (hash),
    FOREIGN KEY (prompt_snapshot_hash) REFERENCES prompt_snapshots (hash),
    UNIQUE (session_id, id)
);

CREATE INDEX turns_session_state_id_index ON turns (session_id, state, id);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    workspace TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    content TEXT NOT NULL,
    model_id TEXT NOT NULL DEFAULT '',
    thinking TEXT NOT NULL DEFAULT '',
    tool_calls_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tool_calls_json)),
    tool_call_id TEXT NOT NULL DEFAULT '',
    is_error BOOLEAN NOT NULL DEFAULT FALSE,
    incomplete BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, turn_id) REFERENCES turns (session_id, id),
    UNIQUE (session_id, id),
    UNIQUE (session_id, sequence)
);

CREATE INDEX messages_session_sequence_id_index ON messages (session_id, sequence, id);
CREATE INDEX messages_turn_sequence_id_index ON messages (turn_id, sequence, id);

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
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, turn_id) REFERENCES turns (session_id, id),
    UNIQUE (session_id, id),
    UNIQUE (session_id, sequence)
);

CREATE INDEX events_session_sequence_id_index ON events (session_id, sequence, id);

CREATE TABLE compactions (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    from_message_id TEXT NOT NULL,
    to_message_id TEXT NOT NULL,
    from_sequence INTEGER NOT NULL CHECK (from_sequence > 0),
    to_sequence INTEGER NOT NULL CHECK (to_sequence >= from_sequence),
    summary TEXT NOT NULL,
    source_message_count INTEGER NOT NULL CHECK (source_message_count > 0),
    input_token_count INTEGER NOT NULL CHECK (input_token_count >= 0),
    summary_token_count INTEGER NOT NULL CHECK (summary_token_count >= 0),
    model_id TEXT NOT NULL,
    prompt_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    supersedes_compaction_id TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, from_message_id) REFERENCES messages (session_id, id),
    FOREIGN KEY (session_id, to_message_id) REFERENCES messages (session_id, id),
    FOREIGN KEY (session_id, supersedes_compaction_id)
        REFERENCES compactions (session_id, id),
    UNIQUE (session_id, id),
    UNIQUE (supersedes_compaction_id)
);

CREATE INDEX compactions_session_to_sequence_id_index
    ON compactions (session_id, to_sequence DESC, id);
