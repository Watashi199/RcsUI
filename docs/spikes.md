# Jalon M0 — résultats des spikes

Mesures effectuées le 2026-11-21 (Go 1.26.5 local, rclone v1.76.0, rsync 3.2.7 sous WSL Ubuntu).
Code : `spikes/rclone/main.go`, `spikes/rsync/capture.sh`, `spikes/e2e/linux.sh`.

## librclone

| Question du plan | Résultat | Conséquence |
|---|---|---|
| `RCLONE_CONFIG` posé par `os.Setenv` avant `librclone.Initialize()` | **Ignoré** : le chemin est figé à l'init du package `fs/config`. | On appelle `config.SetConfigPath(path)` **avant** `Initialize()` (`internal/engine/rclone/lib.go`). Vérifié par `config/paths`. |
| `_config.DryRun` | ✅ Fonctionne. Chaque opération est loguée « `Skipped copy as --dry-run is set (size 1.953Ki)` » (niveau NOTICE). | Les items de dry-run sont déduits des logs (`ItemFromLog`). |
| `_group` personnalisé + `core/stats {group}` | ✅ Stats isolées par run. | Groupe `run/<ulid>`. |
| `job/stop` | ✅ Arrêt en ~600 ms ; `job/status` → `success=false`, `error="context canceled"`. | Issue `cancelled` décidée par notre drapeau, pas par le message. |
| `core/stats-delete` | ✅ Le groupe disparaît de `core/group-list`. | Appelé en fin de run (sinon fuite mémoire). |
| Attribution des logs au run | ✅ `fs.SetLogger(handler)` ; le `ctx` porte le groupe (`accounting.StatsGroupFromContext`). « Copied (new) », « Deleted » et les erreurs de transfert sont attribués. Non attribués : messages globaux (bwlimit, suppression des `.partial` après annulation). | Le repli prévu au plan n'est pas nécessaire dans les cas courants. |
| `JobExpireDuration` | ✅ Réglable via `options/set {"rc":{"JobExpireDuration":"10m"}}`. | Fait à l'initialisation. |
| `BwLimit` par appel | Le limiteur est un **token bucket global** (`fs/accounting/token_bucket.go`). | Pas de limite de bande passante par job rclone, seulement globale (`core/bwlimit`). Option non exposée dans le formulaire rclone. |
| Chaînes `fs` à la volée (`:memory:`) | ⚠️ Acceptées par rc (status 200). | Confirmé : nos chaînes sont construites depuis des `Endpoint` typés (`BuildFs`), jamais reçues brutes. |
| Champs `_config` / `_filter` | Vérifiés dans `fs/config.go` et `fs/filter/*.go` : `DryRun`, `Transfers`, `Checkers`, `CheckSum`, `SizeOnly`, `IgnoreExisting`, `UpdateOlder` (pas `Update`), `TrackRenames`, `MaxDelete` ; filtres `FilterRule`, `MaxAge`, `MinAge`, `MaxSize`, `MinSize`, `DeleteExcluded`. | Utilisés tels quels. |
| Version affichée | `v1.76.0-DEV` sans ldflags (module importé). | `-X github.com/rclone/rclone/fs.Version=v1.76.0` dans le Makefile et le Dockerfile. |

### Taille du binaire (linux/amd64, `-trimpath -ldflags "-s -w"`)

| Backends importés | Taille | Build (cache de modules chaud) |
|---|---|---|
| `backend/all` + `fs/sync` + `fs/operations` | **76,4 Mo** | 53 s |
| `backend/local` + `backend/memory` seulement | **15,9 Mo** | — |

Choix par défaut : `backend/all` (en homelab, la taille compte peu face au nombre de backends disponibles). Pour
réduire le binaire, remplacer l'import dans `internal/engine/rclone/backends.go` par une liste choisie.

## rsync (fixtures `testdata/rsync/3.2.7/`)

- `--no-human-readable` supprime les séparateurs de milliers des **octets**, mais le **débit** reste en `kB/s` / `MB/s`
  (base 1024) : le parser convertit.
- Les lignes de progression se terminent par des espaces puis `\r` ; la dernière ligne avant un item finit par `\n`.
- `%i` vaut 11 caractères, y compris `*deleting  ` : le format `::I %i %l %n` est analysable sans ambiguïté.
- Noms : `\n` est échappé en `\#012` ; l'UTF-8 sort brut sous une locale UTF-8 (on force `LC_ALL=C.UTF-8`).
- Messages hors items sur stdout : `created directory …`, statistiques `--info=stats2`.
- Codes de sortie observés : 0, 1 (option inconnue), 20 (SIGTERM), 23 (fichier illisible, source absente).
- Le format progress2 visé par la regex du plan est confirmé (`(xfr#N, to-chk=R/T)`).

## Bout en bout sous Linux (`spikes/e2e/linux.sh`, binaire réel + rsync 3.2.7)

| Scénario | Résultat |
|---|---|
| Dry-run d'un sync (exclusion `big.bin`) | ✅ 20 items prévus, destination non créée |
| Sync réel | ✅ 20 fichiers, `filesDone=20` |
| Annulation via l'API (bwlimit 2 Mo/s) | ✅ `cancelled`, aucun processus rsync restant |
| `kill -9` du serveur pendant un run | ✅ rsync tué par `Pdeathsig` ; au redémarrage le run passe `interrupted` (« server restarted ») |
| SIGTERM | ✅ arrêt gracieux |

## rsync 3.5.2 : changement de format de la progression

Fixtures `testdata/rsync/3.5.2/` capturées dans `alpine:3.22`. Avec `--no-human-readable`, rsync 3.5.2 écrit
`32768B   0%       0B/s` (suffixe `B` sur les octets, débit brut en `B/s`) au lieu de `32768   0%    0.00kB/s`
en 3.2.7. La regex du parser accepte désormais les deux formats ; les tests de fixtures tournent sur les deux versions.
Sans ce correctif, la progression live aurait été vide dans l'image Docker.

## rsync over SSH (`spikes/e2e/ssh.sh`, sshd OpenSSH 9.6 sur 127.0.0.1:2222)

| Scénario | Résultat |
|---|---|
| Job vers un hôte non approuvé | ✅ refusé à la validation (« not in known hosts ») |
| Scan des clés d'hôte | ✅ ed25519, ecdsa, rsa ; empreinte ed25519 identique à `ssh-keygen -lf` côté serveur |
| `/ssh/test` avec une identité générée | ✅ `ok` |
| Dry-run puis sync (`-z`) | ✅ 10 items prévus sans écriture, puis 10 fichiers transférés |
| Clé privée déchiffrée après le run | ✅ supprimée |
| Clé d'hôte retirée | ✅ lancement refusé |

## Image Docker

- `alpine:3.22` fournit **rsync 3.5.2** (CVE-2026-119741 corrigée) et OpenSSH 10.0 ; image : **141 Mo**.
- Test de fumée : PUID/PGID appliqués, processus `rcsui` non-root sous tini, conteneur `healthy`, `/data` en 0700,
  `secret.key` et `rclone.conf` en 0600, job rsync avec progression live correcte, UI servie.

## `-race` (Go 1.27.4, Linux)

Tous les packages passent, **sauf `internal/engine/rclone`** : data race **dans rclone v1.76.0**
(`fs/accounting/stats.go`, `RemoteStats` lit `s.errors` et `s.lastError` après `s.mu.RUnlock()`, en concurrence
avec `StatsInfo.Error()`). Déclenchée par tout appel `core/stats` pendant un transfert en erreur. Notre code n'y est
pour rien ; signalé en amont : [rclone#10424](https://github.com/rclone/rclone/issues/10424). La CI exécute ce package sans `-race`.

## Non vérifié

- ~~Fixtures rsync 3.4.x~~ : capturées le 2026-11-22 avec rsync 3.4.3 (`alpine:3.20`, utilisateur non-root). Même
  format de progression que la 3.2.7 (`kB/s`) ; mêmes codes de sortie ; le parser passe sans modification.
- Build arm64 de l'image (fait par la CI via QEMU, pas en local).
