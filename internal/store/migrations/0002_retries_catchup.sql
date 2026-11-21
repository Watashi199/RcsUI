-- 0002 : jalon M7 — retries automatiques et rattrapage des exécutions cron manquées.

-- Nombre max de relances automatiques après un échec « retryable » (0 = désactivé).
ALTER TABLE job_definitions ADD COLUMN retry_max     INTEGER NOT NULL DEFAULT 0;
-- Délai avant chaque relance, en secondes.
ALTER TABLE job_definitions ADD COLUMN retry_delay_s INTEGER NOT NULL DEFAULT 300;
-- Au démarrage, exécuter une fois un job planifié dont une occurrence a été manquée.
ALTER TABLE job_definitions ADD COLUMN catch_up      INTEGER NOT NULL DEFAULT 0;

-- Numéro de tentative (1 = premier essai, 2+ = relance automatique).
ALTER TABLE runs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1;
