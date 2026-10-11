-- A child agent is a separate model context. Its prompt-visible conversation
-- lives here rather than in messages, so a parent-session compaction can never
-- absorb it and a child compaction can never rewrite the parent transcript.
CREATE TABLE agent_run_messages (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    agent_run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    content TEXT NOT NULL,
    model_id TEXT NOT NULL DEFAULT '',
    thinking TEXT NOT NULL DEFAULT '',
    tool_calls_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tool_calls_json)),
    tool_call_id TEXT NOT NULL DEFAULT '',
    compaction_id TEXT,
    is_error BOOLEAN NOT NULL DEFAULT FALSE,
    incomplete BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, agent_run_id)
        REFERENCES agent_runs (session_id, id),
    UNIQUE (session_id, id),
    UNIQUE (agent_run_id, id),
    UNIQUE (agent_run_id, sequence)
);

CREATE INDEX agent_run_messages_run_sequence_id_index
    ON agent_run_messages (agent_run_id, sequence, id);

CREATE INDEX agent_run_messages_session_run_index
    ON agent_run_messages (session_id, agent_run_id);

CREATE INDEX agent_run_messages_run_compaction_sequence_id_index
    ON agent_run_messages (agent_run_id, compaction_id, sequence, id);
