ALTER TABLE turns
    ADD COLUMN worker_generation_id TEXT NOT NULL DEFAULT '';

CREATE INDEX turns_worker_generation_id_index
    ON turns (worker_generation_id)
    WHERE worker_generation_id <> '';
