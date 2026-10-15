CREATE TABLE executions (
    id TEXT PRIMARY KEY NOT NULL,
    session_id TEXT NOT NULL,
    root_turn_id TEXT NOT NULL,
    root_execution_id TEXT NOT NULL,
    parent_execution_id TEXT,
    agent_run_id TEXT,
    kind TEXT NOT NULL CHECK (kind IN ('root', 'child')),
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'paused', 'reconciling', 'completed', 'failed', 'cancelled')),
    workflow_version TEXT NOT NULL,
    workflow_execution_id TEXT NOT NULL DEFAULT '',
    task TEXT NOT NULL,
    working_directory TEXT NOT NULL,
    context_json TEXT NOT NULL CHECK (json_valid(context_json)),
    event_count INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0),
    control_revision INTEGER NOT NULL DEFAULT 0 CHECK (control_revision >= 0),
    pause_reason TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    paused_at DATETIME,
    completed_at DATETIME,
    FOREIGN KEY (session_id, root_turn_id) REFERENCES turns (session_id, id),
    FOREIGN KEY (session_id, root_execution_id, root_turn_id) REFERENCES executions (session_id, id, root_turn_id),
    FOREIGN KEY (session_id, parent_execution_id, root_turn_id) REFERENCES executions (session_id, id, root_turn_id),
    FOREIGN KEY (session_id, agent_run_id) REFERENCES agent_runs (session_id, id),
    UNIQUE (session_id, id),
    UNIQUE (session_id, id, root_turn_id),
    UNIQUE (agent_run_id),
    CHECK ((kind = 'root' AND parent_execution_id IS NULL AND agent_run_id IS NULL AND root_execution_id = id) OR
           (kind = 'child' AND parent_execution_id IS NOT NULL AND agent_run_id IS NOT NULL AND root_execution_id <> id)),
    CHECK (parent_execution_id IS NULL OR parent_execution_id <> id)
);

CREATE INDEX executions_session_created_at_id_index
    ON executions (session_id, created_at DESC, id DESC);
CREATE INDEX executions_state_created_at_id_index
    ON executions (state, created_at DESC, id DESC);
CREATE INDEX executions_root_created_at_id_index
    ON executions (root_execution_id, created_at ASC, id ASC);
CREATE INDEX executions_parent_created_at_id_index
    ON executions (parent_execution_id, created_at ASC, id ASC);
CREATE UNIQUE INDEX executions_root_turn_unique_index
    ON executions (root_turn_id) WHERE kind = 'root';
