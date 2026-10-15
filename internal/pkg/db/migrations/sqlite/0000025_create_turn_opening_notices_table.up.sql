CREATE TABLE turn_opening_notices (
    session_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    notice_id TEXT NOT NULL,
    source_sequence INTEGER NOT NULL CHECK (source_sequence > 0),
    PRIMARY KEY (turn_id, notice_id),
    UNIQUE (notice_id),
    UNIQUE (turn_id, source_sequence),
    FOREIGN KEY (session_id, turn_id) REFERENCES turns (session_id, id),
    FOREIGN KEY (session_id, notice_id) REFERENCES session_notices (session_id, id)
);
