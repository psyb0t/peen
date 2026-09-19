DROP INDEX IF EXISTS agent_runs_worker_generation_id_index;

ALTER TABLE agent_runs DROP COLUMN worker_generation_id;

DROP INDEX IF EXISTS jobs_worker_generation_id_index;

ALTER TABLE jobs DROP COLUMN worker_generation_id;

DROP INDEX IF EXISTS events_worker_generation_id_index;

ALTER TABLE events DROP COLUMN worker_generation_id;
