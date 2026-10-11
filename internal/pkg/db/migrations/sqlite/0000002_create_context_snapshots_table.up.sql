CREATE TABLE context_snapshots (
    hash TEXT PRIMARY KEY,
    manifest_json TEXT NOT NULL CHECK (json_valid(manifest_json)),
    resolved_content TEXT NOT NULL,
    created_at DATETIME NOT NULL
);
