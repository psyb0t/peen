-- One immutable summary covering a completed prefix of one child transcript.
--
-- supersedes_compaction_id names the direct predecessor, so a later child
-- compaction extends the chain instead of rewriting earlier direct membership.
-- direct_from_sequence and direct_to_sequence record only the rows this row
-- covered itself; from_sequence and to_sequence span the whole superseded
-- lineage.
CREATE TABLE agent_run_compactions (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    agent_run_id TEXT NOT NULL,
    from_message_id TEXT NOT NULL,
    to_message_id TEXT NOT NULL,
    from_sequence INTEGER NOT NULL CHECK (from_sequence > 0),
    to_sequence INTEGER NOT NULL CHECK (to_sequence >= from_sequence),
    direct_from_sequence INTEGER NOT NULL CHECK (direct_from_sequence > 0),
    direct_to_sequence INTEGER NOT NULL
        CHECK (direct_to_sequence >= direct_from_sequence),
    summary TEXT NOT NULL,
    source_message_count INTEGER NOT NULL CHECK (source_message_count > 0),
    input_token_count INTEGER NOT NULL CHECK (input_token_count >= 0),
    summary_token_count INTEGER NOT NULL CHECK (summary_token_count >= 0),
    model_id TEXT NOT NULL,
    prompt_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    supersedes_compaction_id TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, agent_run_id)
        REFERENCES agent_runs (session_id, id),
    FOREIGN KEY (agent_run_id, from_message_id)
        REFERENCES agent_run_messages (agent_run_id, id),
    FOREIGN KEY (agent_run_id, to_message_id)
        REFERENCES agent_run_messages (agent_run_id, id),
    FOREIGN KEY (agent_run_id, supersedes_compaction_id)
        REFERENCES agent_run_compactions (agent_run_id, id),
    UNIQUE (session_id, id),
    UNIQUE (agent_run_id, id),
    UNIQUE (supersedes_compaction_id)
);

CREATE INDEX agent_run_compactions_run_created_at_id_index
    ON agent_run_compactions (agent_run_id, created_at, id);

CREATE INDEX agent_run_compactions_session_run_index
    ON agent_run_compactions (session_id, agent_run_id);
