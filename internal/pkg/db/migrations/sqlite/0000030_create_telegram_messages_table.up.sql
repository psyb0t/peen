CREATE TABLE telegram_messages (
    chat_id INTEGER NOT NULL CHECK (chat_id <> 0),
    message_id INTEGER NOT NULL CHECK (message_id > 0),
    cron_run_id TEXT,
    PRIMARY KEY (chat_id, message_id),
    FOREIGN KEY (cron_run_id) REFERENCES cron_runs (id)
);
