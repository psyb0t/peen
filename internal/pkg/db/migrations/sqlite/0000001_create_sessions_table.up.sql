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
    model_id TEXT NOT NULL,
    workspace TEXT NOT NULL DEFAULT '',
    agent_space TEXT NOT NULL DEFAULT '',
    stop_revision INTEGER NOT NULL DEFAULT 0 CHECK (stop_revision >= 0)
);

CREATE UNIQUE INDEX sessions_workspace_unique_index
    ON sessions (workspace)
    WHERE workspace <> '';
