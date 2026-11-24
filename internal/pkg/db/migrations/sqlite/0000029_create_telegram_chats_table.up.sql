CREATE TABLE telegram_chats (
    chat_id INTEGER PRIMARY KEY NOT NULL CHECK (chat_id <> 0),
    session_id TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    reasoning_effort TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions (id)
);
