ALTER TABLE messages
    ADD COLUMN injected_kind TEXT NOT NULL DEFAULT '' CHECK (injected_kind IN ('', 'skill'));
