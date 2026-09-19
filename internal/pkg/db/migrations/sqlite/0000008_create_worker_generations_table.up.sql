CREATE TABLE worker_generations (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('native', 'docker')),
    profile TEXT NOT NULL,
    profile_revision INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK (
        state IN (
            'requested',
            'starting',
            'ready',
            'stopping',
            'stopped',
            'failed'
        )
    ),
    workspace TEXT NOT NULL,
    credential_hash TEXT NOT NULL DEFAULT '',
    socket_path TEXT NOT NULL DEFAULT '',
    process_id INTEGER NOT NULL DEFAULT 0,
    container_id TEXT NOT NULL DEFAULT '',
    image_digest TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    exit_code INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    started_at DATETIME,
    ended_at DATETIME,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    UNIQUE (session_id, id)
);

CREATE INDEX worker_generations_session_created_at_id_index
    ON worker_generations (session_id, created_at DESC, id DESC);

CREATE INDEX worker_generations_session_state_index
    ON worker_generations (session_id, state);
