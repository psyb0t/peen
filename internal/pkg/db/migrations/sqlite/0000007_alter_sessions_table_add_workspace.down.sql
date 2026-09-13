DROP INDEX IF EXISTS sessions_workspace_unique_index;

ALTER TABLE sessions DROP COLUMN workspace;
