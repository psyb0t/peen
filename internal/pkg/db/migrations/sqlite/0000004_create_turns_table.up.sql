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
    worker_generation_id TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (context_snapshot_hash) REFERENCES context_snapshots (hash),
    FOREIGN KEY (prompt_snapshot_hash) REFERENCES prompt_snapshots (hash),
    UNIQUE (session_id, id)
);

CREATE INDEX turns_session_state_id_index ON turns (session_id, state, id);

CREATE INDEX turns_worker_generation_id_index
    ON turns (worker_generation_id)
    WHERE worker_generation_id <> '';
