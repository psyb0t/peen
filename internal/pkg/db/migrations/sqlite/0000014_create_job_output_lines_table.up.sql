CREATE TABLE job_output_lines (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    stream TEXT NOT NULL CHECK (stream IN ('stdout', 'stderr')),
    content TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    FOREIGN KEY (session_id, job_id) REFERENCES jobs (session_id, id),
    UNIQUE (session_id, id),
    UNIQUE (job_id, sequence)
);

CREATE INDEX job_output_lines_job_sequence_id_index
    ON job_output_lines (job_id, sequence ASC, id ASC);
