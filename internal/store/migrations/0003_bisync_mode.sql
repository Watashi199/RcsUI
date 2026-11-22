-- rcsui:foreign-keys-off
-- 0003 : mode « bisync » (rclone). SQLite ne sait pas modifier une contrainte
-- CHECK : la table est recréée selon la procédure officielle (ALTER TABLE,
-- §7). Les clés étrangères sont désactivées par le migrateur (marqueur
-- ci-dessus) pour que le DROP ne déclenche ni CASCADE sur job_grants ni
-- SET NULL sur runs ; foreign_key_check est vérifié avant le commit.

CREATE TABLE job_definitions_new (
  id                INTEGER PRIMARY KEY,
  name              TEXT NOT NULL UNIQUE,
  engine            TEXT NOT NULL CHECK (engine IN ('rsync','rclone')),
  mode              TEXT NOT NULL CHECK (mode IN ('copy','sync','move','bisync')),
  source_json       TEXT NOT NULL,
  destination_json  TEXT NOT NULL,
  options_json      TEXT NOT NULL DEFAULT '{}',
  ssh_identity_id   INTEGER REFERENCES ssh_identities(id) ON DELETE RESTRICT,
  schedule_cron     TEXT,
  schedule_on       INTEGER NOT NULL DEFAULT 0,
  retry_interrupted INTEGER NOT NULL DEFAULT 0,
  owner_id          INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  retry_max         INTEGER NOT NULL DEFAULT 0,
  retry_delay_s     INTEGER NOT NULL DEFAULT 300,
  catch_up          INTEGER NOT NULL DEFAULT 0
);

INSERT INTO job_definitions_new (id, name, engine, mode, source_json, destination_json, options_json,
  ssh_identity_id, schedule_cron, schedule_on, retry_interrupted, owner_id, created_at, updated_at,
  retry_max, retry_delay_s, catch_up)
SELECT id, name, engine, mode, source_json, destination_json, options_json,
  ssh_identity_id, schedule_cron, schedule_on, retry_interrupted, owner_id, created_at, updated_at,
  retry_max, retry_delay_s, catch_up
FROM job_definitions;

DROP TABLE job_definitions;
ALTER TABLE job_definitions_new RENAME TO job_definitions;
