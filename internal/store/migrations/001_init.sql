CREATE TABLE video_files (
    id       INTEGER PRIMARY KEY,
    path     TEXT    NOT NULL UNIQUE,
    name     TEXT    NOT NULL,
    size_mb  INTEGER NOT NULL,
    modified INTEGER NOT NULL, -- unix millis
    added    INTEGER NOT NULL  -- unix millis
);

CREATE TABLE tracks (
    id            INTEGER PRIMARY KEY,
    video_file_id INTEGER NOT NULL REFERENCES video_files (id) ON DELETE CASCADE,
    kind          TEXT    NOT NULL,
    idx           INTEGER NOT NULL,
    codec         TEXT    NOT NULL,
    codec_tag     TEXT    NOT NULL DEFAULT '',
    profile       TEXT    NOT NULL DEFAULT '',
    resolution    TEXT    NOT NULL DEFAULT '',
    aspect_ratio  TEXT    NOT NULL DEFAULT '',
    frame_rate    REAL    NOT NULL DEFAULT 0,
    pixel_format  TEXT    NOT NULL DEFAULT '',
    bit_rate      INTEGER NOT NULL DEFAULT 0,
    channels      INTEGER NOT NULL DEFAULT 0,
    layout        TEXT    NOT NULL DEFAULT '',
    sample_rate   TEXT    NOT NULL DEFAULT '',
    language      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_tracks_file ON tracks (video_file_id);

CREATE TABLE completed_jobs (
    id          INTEGER PRIMARY KEY,
    job_id      TEXT    NOT NULL UNIQUE,
    status      TEXT    NOT NULL,
    file_path   TEXT    NOT NULL,
    file_name   TEXT    NOT NULL,
    started_at  INTEGER NOT NULL, -- unix seconds
    duration_ms INTEGER NOT NULL,
    error       TEXT    NOT NULL DEFAULT ''
);
