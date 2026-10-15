CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    tool_call_id TEXT NOT NULL DEFAULT '',
    pid INTEGER NOT NULL CHECK (pid > 0),
    purpose TEXT NOT NULL,
    command TEXT NOT NULL,
    directory TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('running', 'exited', 'signalled', 'failed', 'interrupted')),
    exit_code INTEGER NOT NULL DEFAULT -1,
    failure_detail TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL,
    ended_at DATETIME,
    worker_generation_id TEXT NOT NULL DEFAULT '',
    agent_run_id TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, turn_id) REFERENCES turns (session_id, id),
    FOREIGN KEY (session_id, agent_run_id) REFERENCES agent_runs (session_id, id),
    UNIQUE (session_id, id)
);

CREATE INDEX jobs_session_started_at_id_index
    ON jobs (session_id, started_at DESC, id DESC);
CREATE INDEX jobs_session_state_started_at_id_index
    ON jobs (session_id, state, started_at DESC, id DESC);

CREATE INDEX jobs_worker_generation_id_index
    ON jobs (worker_generation_id)
    WHERE worker_generation_id <> '';
