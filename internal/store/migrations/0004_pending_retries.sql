-- 0004 : jalon M9.1 — les relances automatiques programmées survivent au redémarrage.

-- Une relance en attente au plus par job ; supprimée avec le job.
CREATE TABLE pending_retries (
  job_id    INTEGER PRIMARY KEY REFERENCES job_definitions(id) ON DELETE CASCADE,
  retry_at  INTEGER NOT NULL,           -- heure prévue (ms Unix)
  attempt   INTEGER NOT NULL,           -- numéro de la tentative à lancer (2+)
  resync    INTEGER NOT NULL DEFAULT 0  -- la relance d'un resync bisync reste un resync
);
