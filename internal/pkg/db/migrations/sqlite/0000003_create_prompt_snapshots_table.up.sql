CREATE TABLE prompt_snapshots (
    hash TEXT PRIMARY KEY,
    effective_prompt TEXT NOT NULL,
    created_at DATETIME NOT NULL
);
