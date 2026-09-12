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
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    FOREIGN KEY (session_id, turn_id) REFERENCES turns (session_id, id),
    UNIQUE (session_id, id)
);

CREATE INDEX jobs_session_started_at_id_index
    ON jobs (session_id, started_at DESC, id DESC);
CREATE INDEX jobs_session_state_started_at_id_index
    ON jobs (session_id, state, started_at DESC, id DESC);

CREATE TABLE job_output_lines (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    stream TEXT NOT NULL CHECK (stream IN ('stdout', 'stderr')),
    content TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    FOREIGN KEY (session_id, job_id) REFERENCES jobs (session_id, id),
    UNIQUE (session_id, id),
    UNIQUE (job_id, sequence)
);

CREATE INDEX job_output_lines_job_sequence_id_index
    ON job_output_lines (job_id, sequence ASC, id ASC);

CREATE TABLE job_signal_requests (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    signal TEXT NOT NULL CHECK (signal IN ('stop', 'kill')),
    accepted BOOLEAN NOT NULL,
    state_at_request TEXT NOT NULL CHECK (state_at_request IN ('running', 'exited', 'signalled', 'failed', 'interrupted')),
    requested_at DATETIME NOT NULL,
    FOREIGN KEY (session_id, job_id) REFERENCES jobs (session_id, id),
    UNIQUE (session_id, id)
);

CREATE INDEX job_signal_requests_job_requested_at_id_index
    ON job_signal_requests (job_id, requested_at ASC, id ASC);
