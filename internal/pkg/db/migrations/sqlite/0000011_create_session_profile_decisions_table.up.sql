CREATE TABLE session_profile_decisions (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    from_profile TEXT NOT NULL DEFAULT '',
    to_profile TEXT NOT NULL,
    reason TEXT NOT NULL,
    decided_at DATETIME NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    UNIQUE (session_id, id)
);

CREATE INDEX session_profile_decisions_session_decided_at_id_index
    ON session_profile_decisions (session_id, decided_at DESC, id DESC);
