CREATE TABLE content_blobs (
    hash TEXT PRIMARY KEY NOT NULL CHECK (length(hash) = 64),
    data BLOB NOT NULL
);
