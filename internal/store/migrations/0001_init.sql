CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash BLOB NOT NULL,
    rev           INTEGER NOT NULL DEFAULT 0
);

-- created_by is NULL for the setup invite that creates the first account.
CREATE TABLE invites (
    code_hash  TEXT PRIMARY KEY,
    created_by INTEGER REFERENCES users (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_by    INTEGER REFERENCES users (id) ON DELETE SET NULL,
    used_at    INTEGER
);

CREATE TABLE tokens (
    hash         TEXT PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL
);

CREATE TABLE devices (
    code_hash  TEXT PRIMARY KEY,
    user_code  TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    user_id    INTEGER REFERENCES users (id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL
);

CREATE TABLE watched (
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    type       TEXT NOT NULL CHECK (type IN ('movie', 'show')),
    id         TEXT NOT NULL,
    season     INTEGER,
    episode    INTEGER,
    plays      INTEGER NOT NULL CHECK (plays > 0),
    watched_at INTEGER NOT NULL,
    rev        INTEGER NOT NULL,
    deleted    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, key)
);

-- One row per counted viewing, for history and the calendar. Not synced: clients see the watched aggregate.
CREATE TABLE plays (
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    type       TEXT NOT NULL CHECK (type IN ('movie', 'show')),
    id         TEXT NOT NULL,
    season     INTEGER,
    episode    INTEGER,
    watched_at INTEGER NOT NULL
);

CREATE TABLE progress (
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    type       TEXT NOT NULL CHECK (type IN ('movie', 'show')),
    id         TEXT NOT NULL,
    season     INTEGER,
    episode    INTEGER,
    position   INTEGER NOT NULL CHECK (position >= 0),
    duration   INTEGER NOT NULL CHECK (duration > 0),
    updated_at INTEGER NOT NULL,
    rev        INTEGER NOT NULL,
    deleted    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, key)
);

CREATE TABLE ratings (
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key      TEXT NOT NULL,
    type     TEXT NOT NULL CHECK (type IN ('movie', 'show')),
    id       TEXT NOT NULL,
    season   INTEGER,
    episode  INTEGER,
    rating   INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 10),
    rated_at INTEGER NOT NULL,
    rev      INTEGER NOT NULL,
    deleted  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, key)
);

CREATE TABLE lists (
    user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    slug    TEXT NOT NULL,
    name    TEXT NOT NULL,
    rev     INTEGER NOT NULL,
    deleted INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, slug)
);

CREATE TABLE list_items (
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    list     TEXT NOT NULL,
    key      TEXT NOT NULL,
    type     TEXT NOT NULL CHECK (type IN ('movie', 'show')),
    id       TEXT NOT NULL,
    season   INTEGER,
    episode  INTEGER,
    added_at INTEGER NOT NULL,
    rev      INTEGER NOT NULL,
    deleted  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, list, key)
);

CREATE TABLE meta (
    user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key     TEXT NOT NULL,
    type    TEXT NOT NULL CHECK (type IN ('movie', 'show')),
    id      TEXT NOT NULL,
    title   TEXT NOT NULL DEFAULT '',
    year    INTEGER NOT NULL DEFAULT 0,
    poster  TEXT NOT NULL DEFAULT '',
    rev     INTEGER NOT NULL,
    deleted INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, key)
);

-- profile is NULL between signing in and picking which Nuvio profile to mirror.
CREATE TABLE nuvio_links (
    user_id         INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    backend         TEXT NOT NULL,
    api_key         TEXT NOT NULL,
    email           TEXT NOT NULL,
    profile         INTEGER,
    profile_name    TEXT NOT NULL DEFAULT '',
    access_token    TEXT NOT NULL,
    refresh_token   TEXT NOT NULL,
    expires_at      INTEGER NOT NULL,
    progress_cursor INTEGER NOT NULL DEFAULT 0,
    watched_cursor  INTEGER NOT NULL DEFAULT 0,
    library_cursor  INTEGER NOT NULL DEFAULT 0,
    events_applied  INTEGER NOT NULL DEFAULT 0,
    synced_at       INTEGER,
    sync_error      TEXT NOT NULL DEFAULT '',
    session_expired INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX watched_rev ON watched (user_id, rev);
CREATE INDEX progress_rev ON progress (user_id, rev);
CREATE INDEX ratings_rev ON ratings (user_id, rev);
CREATE INDEX lists_rev ON lists (user_id, rev);
CREATE INDEX list_items_rev ON list_items (user_id, rev);
CREATE INDEX meta_rev ON meta (user_id, rev);
CREATE INDEX watched_content ON watched (user_id, type, id);
CREATE INDEX list_items_content ON list_items (user_id, type, id);
CREATE TABLE imports (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    source      TEXT NOT NULL,
    label       TEXT NOT NULL,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    section     TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE import_sections (
    import_id INTEGER NOT NULL REFERENCES imports (id) ON DELETE CASCADE,
    position  INTEGER NOT NULL,
    section   TEXT NOT NULL,
    imported  INTEGER NOT NULL,
    no_id     INTEGER NOT NULL,
    skipped   INTEGER NOT NULL,
    PRIMARY KEY (import_id, position)
);

CREATE INDEX tokens_user ON tokens (user_id);
CREATE INDEX imports_latest ON imports (user_id, source, started_at);
CREATE INDEX plays_recent ON plays (user_id, watched_at);
CREATE INDEX plays_item ON plays (user_id, key, watched_at);
