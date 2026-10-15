CREATE TABLE execution_turn_leases (
    execution_id TEXT PRIMARY KEY NOT NULL,
    session_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    lease_id TEXT NOT NULL UNIQUE,
    owner_generation_id TEXT NOT NULL,
    control_revision INTEGER NOT NULL CHECK (control_revision >= 0),
    worker_generation_id TEXT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    FOREIGN KEY (session_id, execution_id, turn_id) REFERENCES executions (session_id, id, root_turn_id),
    FOREIGN KEY (session_id, worker_generation_id) REFERENCES worker_generations (session_id, id)
);
