ALTER TABLE events
    ADD COLUMN worker_generation_id TEXT NOT NULL DEFAULT '';

CREATE INDEX events_worker_generation_id_index
    ON events (worker_generation_id)
    WHERE worker_generation_id <> '';

ALTER TABLE jobs
    ADD COLUMN worker_generation_id TEXT NOT NULL DEFAULT '';

CREATE INDEX jobs_worker_generation_id_index
    ON jobs (worker_generation_id)
    WHERE worker_generation_id <> '';

ALTER TABLE agent_runs
    ADD COLUMN worker_generation_id TEXT NOT NULL DEFAULT '';

CREATE INDEX agent_runs_worker_generation_id_index
    ON agent_runs (worker_generation_id)
    WHERE worker_generation_id <> '';
