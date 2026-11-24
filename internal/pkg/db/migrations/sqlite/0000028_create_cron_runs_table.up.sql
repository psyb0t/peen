CREATE TABLE cron_runs (
    id TEXT PRIMARY KEY NOT NULL,
    job_name TEXT NOT NULL CHECK (length(job_name) BETWEEN 1 AND 64),
    scheduled_at DATETIME NOT NULL,
    started_at DATETIME,
    finished_at DATETIME,
    session_id TEXT,
    state TEXT NOT NULL CHECK (state IN ('running', 'completed', 'failed', 'cancelled', 'skipped')),
    error TEXT NOT NULL DEFAULT '' CHECK (length(error) <= 256),
    FOREIGN KEY (session_id) REFERENCES sessions (id),
    UNIQUE (job_name, scheduled_at)
);

CREATE INDEX cron_runs_job_scheduled_at_id_index
    ON cron_runs (job_name, scheduled_at DESC, id DESC);
CREATE INDEX cron_runs_state_scheduled_at_id_index
    ON cron_runs (state, scheduled_at DESC, id DESC);
