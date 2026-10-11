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
