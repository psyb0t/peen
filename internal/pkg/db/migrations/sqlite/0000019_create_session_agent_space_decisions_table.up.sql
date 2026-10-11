CREATE TABLE session_agent_space_decisions (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    from_agent_space TEXT NOT NULL DEFAULT '',
    to_agent_space TEXT NOT NULL,
    reason TEXT NOT NULL,
    decided_at DATETIME NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    UNIQUE (session_id, id)
);

CREATE INDEX session_agent_space_decisions_session_decided_at_id_index
    ON session_agent_space_decisions (session_id, decided_at DESC, id DESC);
