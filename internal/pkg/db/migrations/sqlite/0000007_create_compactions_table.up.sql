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
