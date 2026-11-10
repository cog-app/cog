-- WebDAV write locks (RFC 4918). A lock lives with the files it covers, so
-- every replica serving the user sees it. path is the lock root; a lock with
-- infinite set covers everything beneath it as well.
CREATE TABLE IF NOT EXISTS dav_locks (
    token      TEXT PRIMARY KEY,
    path       TEXT NOT NULL,
    infinite   INTEGER NOT NULL,
    shared     INTEGER NOT NULL,
    owner      TEXT NOT NULL DEFAULT '',
    timeout    INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_dav_locks_path ON dav_locks(path);
