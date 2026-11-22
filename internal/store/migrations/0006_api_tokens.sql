-- 0006 : jetons d'API personnels (M11). Seul le SHA-256 du jeton est stocké ;
-- role borne les droits du jeton (jamais au-delà du rôle courant de l'utilisateur).
CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  token_hash   BLOB NOT NULL UNIQUE,
  prefix       TEXT NOT NULL,
  role         TEXT NOT NULL CHECK (role IN ('admin','operator','viewer')),
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER,
  last_used_at INTEGER,
  UNIQUE (user_id, name)
);
CREATE INDEX api_tokens_user ON api_tokens(user_id);
