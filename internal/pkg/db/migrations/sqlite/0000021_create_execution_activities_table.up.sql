CREATE TABLE execution_activities (
    id TEXT PRIMARY KEY NOT NULL,
    session_id TEXT NOT NULL,
    execution_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('model', 'tool', 'compaction', 'preparation')),
    state TEXT NOT NULL CHECK (state IN ('scheduled', 'running', 'completed', 'failed', 'paused', 'uncertain', 'cancelled')),
    replay_policy TEXT NOT NULL CHECK (replay_policy IN ('retry', 'reconcile')),
    name TEXT NOT NULL,
    input_json TEXT NOT NULL CHECK (json_valid(input_json)),
    output_json TEXT CHECK (output_json IS NULL OR json_valid(output_json)),
    context_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(context_json)),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    owner_generation_id TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    resolution_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(resolution_json)),
    created_at DATETIME NOT NULL,
    started_at DATETIME,
    completed_at DATETIME,
    FOREIGN KEY (session_id, execution_id) REFERENCES executions (session_id, id),
    UNIQUE (execution_id, step_id),
    UNIQUE (session_id, id),
    UNIQUE (session_id, execution_id, id),
    CHECK (state <> 'completed' OR output_json IS NOT NULL)
);

CREATE INDEX execution_activities_execution_created_at_id_index
    ON execution_activities (execution_id, created_at ASC, id ASC);
CREATE INDEX execution_activities_state_owner_index
    ON execution_activities (state, owner_generation_id);
