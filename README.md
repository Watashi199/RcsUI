# RcsUI

Interface web auto-hébergée pour piloter **rclone** (embarqué comme bibliothèque) et **rsync** depuis une seule UI.
Cible : homelab / NAS, quelques utilisateurs, un seul `docker run`. Conception détaillée : [PLAN.md](PLAN.md).

## État

| Jalon | État |
|---|---|
| M0 — spikes | ✅ ([docs/spikes.md](docs/spikes.md)) |
| M1 — MVP (auth, jobs rsync/rclone, runs, SSE, annulation, reprise, racines autorisées) | ✅ |
| M2 — dry-run, explorateur, gestion des remotes (formulaires dynamiques, masquage) | ✅ |
| M3 — planification cron, runs « skipped », rétention | ✅ |
| M4 — rôles admin / operator / viewer, droits par job, par remote et par identité SSH, audit | ✅ |
| M5 — rsync over SSH : clés gérées (ed25519, import, chiffrées AES-GCM), known_hosts approuvés, `~/.ssh` monté | ✅ testé de bout en bout contre un sshd réel |
| M6 — OIDC (code flow + PKCE, rôles par groupes, provisioning optionnel) | ✅ testé contre un IdP simulé |
| M7 — durcissement et confort (CSP par hash, relances automatiques, rattrapage des cron manqués, notifications webhook/ntfy, `/metrics` Prometheus, bisync rclone, interface FR/EN, tests e2e Playwright en CI) | ✅ release **v1.0.0** signée sur GHCR ([docs/release.md](docs/release.md)) |

Suite (maintenance, robustesse, chiffrement de la config rclone, nouvelles fonctions) : [docs/roadmap.md](docs/roadmap.md).

## Fonctionnalités M7

- **Relances automatiques** (par job) : après un échec jugé transitoire par le moteur (rsync : codes 5, 10, 12, 30, 35 ; rclone : erreur non fatale), le job est relancé jusqu'à *N* fois (0–10) après un délai (10 s – 24 h, 300 s par défaut). Le run porte `trigger=retry` et un numéro de tentative. Un lancement manuel ou planifié remplace la relance en attente. Les relances programmées sont enregistrées en base et survivent à un redémarrage ; une relance dont l'heure est passée pendant l'arrêt part au démarrage.
- **Destination exclusive** (par job, désactivée par défaut) : le job ne démarre pas si un autre job actif écrit au même endroit (même dossier ou dossier imbriqué, sur le même système de fichiers ; la source compte aussi pour `move` et `bisync`). Une exécution planifiée ou une relance est alors marquée « Ignoré » avec le nom du job en cause ; un lancement manuel est refusé (409 `destination_busy`).
- **Rattrapage des cron manqués** (par job) : au démarrage, si une occurrence planifiée est tombée pendant l'arrêt du serveur, le job est exécuté **une fois**. Référence : le dernier run planifié, ou la dernière modification du job si elle est plus récente.
- **Notifications** (Réglages → Notifications, admin) : webhook JSON générique (`{event, run, retryAt, url, title, message}`) et/ou ntfy (URL du topic, jeton optionnel). Modes : jamais, problèmes (échec, partiel, interrompu, ignoré) ou tout ; dry-runs exclus par défaut. URL et jeton sont chiffrés avec la clé maître et jamais renvoyés par l'API. Bouton « Envoyer un test ».
- **Synchronisation bidirectionnelle (bisync rclone)** : mode « Bidirectionnelle » d'un job rclone (Chemin 1 ↔ Chemin 2, via `sync/bisync`). Le **premier run doit être un resync** (bouton « Resync » de la page du job, réservé au droit *edit*), de même après un changement de filtres ou une erreur critique ; un resync fusionne les deux côtés et ne supprime rien. Les listings de référence vivent dans `/data/bisync` (à conserver avec le volume). Les filtres sont écrits dans un fichier dont le nom dérive du contenu : les modifier impose un resync au lieu de faire passer des fichiers pour supprimés. Options : résolution des conflits, sort de la version perdante, critères de comparaison, garde-fou de suppressions en %, reprise après interruption (`--recover`, activée par défaut). Une erreur qui exige un resync n'est jamais relancée automatiquement. Le récit de bisync (construction des listings, changements appliqués…) apparaît en direct dans le log du run ; si plusieurs bisyncs tournent en même temps, seuls ses messages importants (NOTICE et plus) y figurent, en fin de run.
- **Langue de l'interface** : français ou anglais, détectée depuis le navigateur et modifiable dans la barre latérale (choix mémorisé par navigateur). Les messages d'erreur renvoyés par l'API restent en anglais.
- **Métriques Prometheus** : `GET /metrics`, monté uniquement si `RCSUI_METRICS_TOKEN` est défini, protégé par `Authorization: Bearer <jeton>`. Séries : `rcsui_build_info`, `rcsui_runs_active{state}`, `rcsui_max_concurrent_runs`, `rcsui_runs_finished_total{engine,state}`, `rcsui_transferred_{bytes,files}_total{engine}`, `rcsui_jobs`, `rcsui_jobs_scheduled`, `rcsui_job_last_success_timestamp_seconds`, `rcsui_job_last_run_{timestamp_seconds,success,duration_seconds}` (labels `job`, `job_id`, `engine`). Les compteurs `_total` repartent de zéro au redémarrage (comportement standard d'un compteur Prometheus).

```yaml
# prometheus.yml
scrape_configs:
  - job_name: rcsui
    authorization: { credentials_file: /etc/prometheus/rcsui_token }
    static_configs: [{ targets: ['rcsui:8080'] }]
```

Alerte type : `time() - rcsui_job_last_success_timestamp_seconds > 2 * 86400`.

## Démarrage rapide (Docker)

```bash
docker run -d --name rcsui -p 8080:8080 --restart unless-stopped --stop-timeout 30 \
  --security-opt no-new-privileges \
  -e PUID=1000 -e PGID=1000 -e TZ=Europe/Paris \
  -e RCSUI_ALLOWED_ROOTS=/mnt/photos,/mnt/backup \
  -v rcsui-data:/data -v /srv/photos:/mnt/photos -v /srv/backup:/mnt/backup \
  rcsui:dev
docker logs rcsui   # mot de passe admin initial (affiché une seule fois)
```

`/data` doit être sur un disque local (SQLite en WAL n'est pas fiable sur NFS/SMB).

Image : `rcsui:dev` est construite localement (`make docker`). Les versions publiées sont sur
`ghcr.io/watashi199/rcsui:<version>` (amd64 et arm64, SBOM et signature cosign) : procédure de release et
vérification dans [docs/release.md](docs/release.md).

### Variables d'environnement

| Variable | Défaut | Rôle |
|---|---|---|
| `RCSUI_LISTEN` | `:8080` | Adresse d'écoute |
| `RCSUI_DATA_DIR` | `/data` | Base SQLite, config rclone, logs des runs, sauvegardes |
| `RCSUI_ALLOWED_ROOTS` | *(vide)* | Racines locales autorisées, séparées par des virgules. Vide : aucun chemin local. |
| `RCSUI_MAX_CONCURRENT` | `2` | Exécutions simultanées (modifiable ensuite dans Réglages) |
| `RCSUI_ADMIN_USER` / `RCSUI_ADMIN_PASSWORD(_FILE)` | `admin` / généré | Premier administrateur |
| `RCSUI_BASE_URL` | — | URL publique (origine de confiance) |
| `RCSUI_TRUSTED_PROXIES` | — | CIDR des reverse proxies dont on lit `X-Forwarded-*` |
| `RCSUI_COOKIE_SECURE` | `auto` | `auto` \| `true` \| `false` |
| `RCSUI_LOG_LEVEL`, `RCSUI_LOG_RETENTION_DAYS` | `info`, `90` | |
| `RCSUI_DISABLE_RCLONE` | `false` | Désactive le moteur rclone |
| `RCSUI_SECRET_KEY(_FILE)` | généré dans `/data/secret.key` | Clé maître AES-256 (base64, 32 octets) chiffrant les clés SSH. **À sauvegarder** : sans elle, les identités SSH sont perdues. |
| `RCSUI_SSH_DIR` | — | `~/.ssh` monté en lecture seule (le dossier doit s'appeler `.ssh`) ; utilisable par les admins |
| `RCSUI_OIDC_ISSUER`, `RCSUI_OIDC_CLIENT_ID`, `RCSUI_OIDC_CLIENT_SECRET(_FILE)` | — | Active OIDC (nécessite `RCSUI_BASE_URL` ; redirect : `<BASE_URL>/api/v1/auth/oidc/callback`) |
| `RCSUI_OIDC_SCOPES`, `RCSUI_OIDC_USERNAME_CLAIM`, `RCSUI_OIDC_ROLE_CLAIM` | `openid,profile,email,groups`, `preferred_username`, `groups` | |
| `RCSUI_OIDC_ADMIN_GROUPS`, `RCSUI_OIDC_OPERATOR_GROUPS` | — | Groupes → rôle, recalculé à chaque connexion (sinon : rôle conservé) |
| `RCSUI_OIDC_AUTO_PROVISION` | `false` | Crée le compte au premier login (rôle `viewer` hors groupes) ; sinon un admin doit créer le compte au préalable |
| `RCSUI_METRICS_TOKEN(_FILE)` | — | Active `GET /metrics` (Prometheus), protégé par ce jeton bearer (16 caractères minimum) |
| `PUID`, `PGID`, `TZ` | `1000`, `1000` | Utilisateur du conteneur (jamais root) |

## Développement

Prérequis : Go ≥ 1.26, Node ≥ 22.17, rsync (tests d'intégration).

```bash
make test          # tests Go + Vitest
make check         # go vet + svelte-check
make build         # binaire unique avec l'UI embarquée -> bin/rcsui
make dev           # backend seul (sans UI embarquée) sur :8080, données dans ./.dev
make dev-web       # Vite avec proxy /api -> :8080
make e2e           # tests Playwright contre l'image (conteneur jetable, Docker requis)
make docker        # image locale
```

Arborescence : `cmd/rcsui` (point d'entrée), `internal/{api,auth,authz,config,engine,events,paths,runner,scheduler,store}`,
`web/` (SvelteKit 3, Svelte 5, adapter-static), `testdata/rsync/` (sorties rsync réelles pour le parser).

## Sécurité, en bref

Aucune commande n'est construite via un shell : argv en tableau, options rsync en liste blanche typée, `--` avant les
chemins. Les chemins locaux sont confinés aux racines autorisées (symlinks résolus, revalidation au lancement). Les
chaînes rclone sont construites côté serveur (pas de backend à la volée). Les secrets des remotes ne sont jamais
renvoyés par l'API. Sessions par cookie `HttpOnly`/`SameSite=Lax`, argon2id, double protection CSRF
(`Sec-Fetch-Site`/`Origin` + jeton), CSP stricte. Détail : [PLAN.md §8](PLAN.md).
