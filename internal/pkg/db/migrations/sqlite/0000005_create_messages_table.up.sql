-- compaction_id references compactions, which is created after messages
-- because compactions references messages. SQLite resolves a foreign key when a
-- row is written, so the forward reference is valid.
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
    compaction_id TEXT,
    injected BOOLEAN NOT NULL DEFAULT FALSE,
    injected_kind TEXT NOT NULL DEFAULT '' CHECK (injected_kind IN ('', 'skill')),
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, turn_id) REFERENCES turns (session_id, id),
    FOREIGN KEY (compaction_id) REFERENCES compactions (id),
    UNIQUE (session_id, id),
    UNIQUE (session_id, sequence)
);

CREATE INDEX messages_session_sequence_id_index ON messages (session_id, sequence, id);
CREATE INDEX messages_turn_sequence_id_index ON messages (turn_id, sequence, id);

CREATE INDEX messages_session_compaction_id_sequence_id_index
    ON messages (session_id, compaction_id, sequence, id);
