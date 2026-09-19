DROP INDEX IF EXISTS turns_worker_generation_id_index;

ALTER TABLE turns DROP COLUMN worker_generation_id;
