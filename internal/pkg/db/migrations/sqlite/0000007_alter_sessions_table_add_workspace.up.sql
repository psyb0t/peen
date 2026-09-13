ALTER TABLE sessions
    ADD COLUMN workspace TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX sessions_workspace_unique_index
    ON sessions (workspace)
    WHERE workspace <> '';
