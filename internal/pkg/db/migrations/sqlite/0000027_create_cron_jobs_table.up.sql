CREATE TABLE cron_jobs (
    job_name TEXT PRIMARY KEY NOT NULL CHECK (length(job_name) BETWEEN 1 AND 64),
    next_scheduled_at DATETIME NOT NULL,
    definition_hash TEXT NOT NULL CHECK (length(definition_hash) = 64)
);
