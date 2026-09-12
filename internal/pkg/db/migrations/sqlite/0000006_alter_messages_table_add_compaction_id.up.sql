ALTER TABLE messages
    ADD COLUMN compaction_id TEXT REFERENCES compactions (id);

UPDATE messages
SET compaction_id = (
    SELECT compaction.id
    FROM compactions AS compaction
    LEFT JOIN compactions AS parent
        ON parent.session_id = compaction.session_id
        AND parent.id = compaction.supersedes_compaction_id
    WHERE compaction.session_id = messages.session_id
      AND messages.sequence BETWEEN
          CASE
              WHEN parent.id IS NULL THEN compaction.from_sequence
              ELSE parent.to_sequence + 1
          END
          AND compaction.to_sequence
    ORDER BY compaction.created_at ASC, compaction.id ASC
    LIMIT 1
);

CREATE INDEX messages_session_compaction_id_sequence_id_index
    ON messages (session_id, compaction_id, sequence, id);
