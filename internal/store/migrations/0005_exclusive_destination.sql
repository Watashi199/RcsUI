-- 0005 : option par job « ne pas démarrer si un autre job écrit dans la même
-- destination » (run « skipped » pour les déclenchements automatiques).
ALTER TABLE job_definitions ADD COLUMN exclusive_dest INTEGER NOT NULL DEFAULT 0;
