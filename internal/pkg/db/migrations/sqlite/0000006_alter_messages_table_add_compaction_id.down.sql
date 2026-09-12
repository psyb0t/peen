DROP INDEX IF EXISTS messages_session_compaction_id_sequence_id_index;

ALTER TABLE messages DROP COLUMN compaction_id;
