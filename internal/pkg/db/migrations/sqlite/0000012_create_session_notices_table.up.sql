CREATE TABLE session_notices (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    type TEXT NOT NULL,
    source TEXT NOT NULL,
    summary TEXT NOT NULL,
    data_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(data_json)),
    delivery TEXT NOT NULL CHECK (delivery IN ('queue', 'wake')),
    state TEXT NOT NULL CHECK (state IN ('pending', 'delivered')),
    created_at DATETIME NOT NULL,
    delivered_at DATETIME,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    UNIQUE (session_id, id),
    UNIQUE (session_id, sequence)
);

CREATE INDEX session_notices_session_state_sequence_id_index
    ON session_notices (session_id, state, sequence, id);

CREATE INDEX session_notices_session_sequence_id_index
    ON session_notices (session_id, sequence, id);
