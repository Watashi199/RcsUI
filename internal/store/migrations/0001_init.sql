-- 0001_init : schéma initial (PLAN.md §2.2)

CREATE TABLE users (
  id              INTEGER PRIMARY KEY,
  username        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash   TEXT,
  role            TEXT NOT NULL CHECK (role IN ('admin','operator','viewer')),
  oidc_issuer     TEXT,
  oidc_subject    TEXT,
  must_change_pw  INTEGER NOT NULL DEFAULT 0,
  disabled        INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  last_login_at   INTEGER,
  UNIQUE (oidc_issuer, oidc_subject)
);

CREATE TABLE sessions (
  token_hash    BLOB PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token    TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  last_seen_at  INTEGER NOT NULL,
  expires_at    INTEGER NOT NULL,
  ip            TEXT,
  user_agent    TEXT
);
CREATE INDEX sessions_user ON sessions(user_id);
CREATE INDEX sessions_exp  ON sessions(expires_at);

CREATE TABLE ssh_identities (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  key_type      TEXT NOT NULL,
  public_key    TEXT NOT NULL,
  private_enc   BLOB NOT NULL,
  fingerprint   TEXT NOT NULL,
  created_at    INTEGER NOT NULL
);

CREATE TABLE ssh_known_hosts (
  id            INTEGER PRIMARY KEY,
  host          TEXT NOT NULL,
  port          INTEGER NOT NULL DEFAULT 22,
  key_type      TEXT NOT NULL,
  public_key    TEXT NOT NULL,
  fingerprint   TEXT NOT NULL,
  added_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at    INTEGER NOT NULL,
  UNIQUE (host, port, key_type)
);

CREATE TABLE job_definitions (
  id                INTEGER PRIMARY KEY,
  name              TEXT NOT NULL UNIQUE,
  engine            TEXT NOT NULL CHECK (engine IN ('rsync','rclone')),
  mode              TEXT NOT NULL CHECK (mode IN ('copy','sync','move')),
  source_json       TEXT NOT NULL,
  destination_json  TEXT NOT NULL,
  options_json      TEXT NOT NULL DEFAULT '{}',
  ssh_identity_id   INTEGER REFERENCES ssh_identities(id) ON DELETE RESTRICT,
  schedule_cron     TEXT,
  schedule_on       INTEGER NOT NULL DEFAULT 0,
  retry_interrupted INTEGER NOT NULL DEFAULT 0,
  owner_id          INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL
);

CREATE TABLE job_grants (
  job_id      INTEGER NOT NULL REFERENCES job_definitions(id) ON DELETE CASCADE,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  permission  TEXT NOT NULL CHECK (permission IN ('view','run','edit')),
  PRIMARY KEY (job_id, user_id)
);

CREATE TABLE remote_grants (
  remote_name TEXT NOT NULL,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (remote_name, user_id)
);

CREATE TABLE ssh_identity_grants (
  identity_id INTEGER NOT NULL REFERENCES ssh_identities(id) ON DELETE CASCADE,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (identity_id, user_id)
);

CREATE TABLE runs (
  id                 TEXT PRIMARY KEY,
  job_id             INTEGER REFERENCES job_definitions(id) ON DELETE SET NULL,
  job_name           TEXT NOT NULL,
  engine             TEXT NOT NULL,
  trigger            TEXT NOT NULL CHECK (trigger IN ('manual','schedule','retry')),
  triggered_by       INTEGER REFERENCES users(id) ON DELETE SET NULL,
  dry_run            INTEGER NOT NULL DEFAULT 0,
  state              TEXT NOT NULL CHECK (state IN
                     ('queued','starting','running','cancelling',
                      'succeeded','partial','failed','cancelled','interrupted','skipped')),
  spec_json          TEXT NOT NULL,
  queued_at          INTEGER NOT NULL,
  started_at         INTEGER,
  finished_at        INTEGER,
  exit_code          INTEGER,
  rclone_job_id      INTEGER,
  pid                INTEGER,
  error_message      TEXT,
  bytes_done         INTEGER NOT NULL DEFAULT 0,
  bytes_total        INTEGER,
  files_done         INTEGER NOT NULL DEFAULT 0,
  files_total        INTEGER,
  files_deleted      INTEGER NOT NULL DEFAULT 0,
  errors_count       INTEGER NOT NULL DEFAULT 0,
  log_path           TEXT,
  log_size           INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX runs_job_time ON runs(job_id, queued_at DESC);
CREATE INDEX runs_state    ON runs(state);
CREATE INDEX runs_time     ON runs(queued_at DESC);
CREATE UNIQUE INDEX runs_one_active_per_job ON runs(job_id)
  WHERE state IN ('queued','starting','running','cancelling');

CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE audit_log (
  id       INTEGER PRIMARY KEY,
  at       INTEGER NOT NULL,
  user_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  action   TEXT NOT NULL,
  target   TEXT,
  details  TEXT
);
CREATE INDEX audit_at ON audit_log(at DESC);
