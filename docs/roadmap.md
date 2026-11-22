# Après la v1.0 — plan

État au 2026-11-22 : M0 à M7 terminés, **v1.0.0** publiée (image GHCR multi-arch signée, attestation de provenance).
Ce document prend la suite de la roadmap de [PLAN.md](../PLAN.md) §11. Complexités : **S** ≈ 2–4 j, **M** ≈ 1–2 sem.

## Principes

- **SemVer strict.** Correctifs et maintenance en `v1.0.x` ; nouvelles fonctions en `v1.x`. Aucune rupture du
  volume `/data` ni de l'API `/api/v1` avant une v2 : chaque migration SQLite reste rejouable depuis la v1.0.0.
- **Une release = un tag** (procédure dans [release.md](release.md)), avec une pre-release `-rc.N` dès qu'une
  release touche au stockage des secrets ou au schéma.
- **Rien sans retour d'usage** pour les fonctions nouvelles (M11) : on les trie après quelques semaines d'usage réel.

## M8 — Tenir les promesses de la v1.0 (v1.0.1) · S — ✅ fait (v1.0.1 publiée le 2026-11-22)

Écarts entre PLAN.md et le dépôt, relevés après la release :

| Point | Constat | Action |
|---|---|---|
| Actions CI en Node 20 | Avertissement de dépréciation sur tous les jobs. | Fait (PR #1) ; job `release` validé par la v1.0.1. Workflow CodeQL avancé retiré : il entrait en conflit avec la configuration par défaut (PR #11). Codacy retiré : redondant avec CodeQL et govulncheck. |
| Renovate (§8, n° 16) | Aucune config dans le dépôt. | **Dependabot** à la place (intégré à GitHub, pas d'application à installer) : `.github/dependabot.yml`, mises à jour regroupées Go / npm / actions / images Docker, `cooldown` de 14 j. |
| Images épinglées par digest (§8, n° 16) | `golang:1.27-alpine`, `node:24-alpine`, `alpine:3.22` par tag. | Épinglées en `tag@sha256:…` (index multi-arch) ; Dependabot met les digests à jour. |
| `go mod verify` (§8, n° 16) | Absent de la CI. | Étape dans le job `go`. |
| Couverture ≥ 80 % (§9) | Non mesurée en CI. Par package, `auth` affichait 51 % : ses tests OIDC vivent dans `internal/api`. | Mesure sur tous les tests (`-coverpkg`, `.github/coverage.sh`), seuil bloquant de 80 % sur les 5 packages ; tests de validation ajoutés à `engine/rsync` (79,9 % → 89,1 %). |
| Data race rclone (spikes.md) | « À signaler en amont » : aucune issue trouvée. | Signalée : [rclone#10424](https://github.com/rclone/rclone/issues/10424). Réactiver `-race` sur `internal/engine/rclone` dès la version corrigée. |
| Fixtures rsync 3.4.x (spikes.md, « Non vérifié ») | Seules 3.2.7 et 3.5.2 sont capturées. | Capturées avec rsync 3.4.3 (`alpine:3.20` ; Debian trixie est déjà en 3.5.0). Le parser passe sans modification. |

Critère de fini : `v1.0.1` publiée par la nouvelle CI, sans avertissement, Dependabot actif.

## M9 — Robustesse (v1.1) · M

1. ✅ **Relances persistées.** Table `pending_retries` (migration 0004), réarmée au démarrage par
   `Manager.RestoreRetries` après `Recover` ; une relance échue pendant l'arrêt part aussitôt. Un nouveau run du
   job la supprime en mémoire et en base. Corrigé au passage : la relance est enregistrée avant l'état final et
   armée dans la même section critique que la libération du job (un lancement manuel ne peut plus s'intercaler).
2. ✅ **Récit bisync dans le log du run.** Cause : bisync journalise son récit sans contexte (`fs.Infof`, 71
   appels en v1.76.0), filtré par le niveau **global** de rclone (NOTICE) avant d'atteindre notre handler.
   Niveau global passé à INFO ; `logHandler` attribue en direct les messages sans groupe au bisync s'il est le
   seul en cours (sinon repli sur le champ `output`, NOTICE et plus, en fin de run) et jette les autres messages
   sans run sous NOTICE (log serveur inchangé). Codes couleur ANSI retirés.
3. ✅ **Planificateur : on garde `robfig/cron/v3`** (décision du 2026-11-22). `go-co-op/gocron/v2` (v2.23.2)
   dépend lui-même de `robfig/cron/v3` v3.0.1 et s'en sert pour analyser les expressions : migrer ne retirerait
   pas la dépendance et n'en remplacerait que la boucle de déclenchement, au prix de deux dépendances de plus.
   Le code reste encapsulé derrière `scheduler.Scheduler`, avec `cron.Recover` explicite.
4. ✅ **Chevauchement de destinations.** Option par job `exclusiveDestination` (migration 0005), désactivée par
   défaut : le job ne démarre pas si un autre job actif (en file ou en cours) écrit au même endroit. Écrits :
   la destination, plus la source pour `move` et les deux côtés pour `bisync` ; même endroit = même système de
   fichiers (local, même remote, même hôte SSH:port) et chemins égaux ou imbriqués, symlinks locaux résolus. Planification et relance
   automatique → run `skipped` avec le nom du job qui occupe la destination ; lancement manuel → 409
   `destination_busy`. L'option ne protège que le job qui l'active (un autre job peut démarrer pendant lui).

## M10 — Secrets rclone chiffrés (v1.2) · M

Risque n° 7 de PLAN.md : `rclone.conf` n'est qu'obscurci (0600, masqué par l'API).

- Chiffrer la config avec le mécanisme natif de rclone (mot de passe de config), mot de passe dérivé de la clé
  maître par HKDF : une seule clé à sauvegarder, comme pour les clés SSH.
- Migration au démarrage : conf en clair → chiffrée, avec copie de sauvegarde dans `/data/backups` ; retour
  arrière documenté.
- À vérifier en spike : que `config/*` via librclone fonctionne avec une conf chiffrée sans invite interactive,
  et l'effet sur les remotes OAuth (rafraîchissement du token qui réécrit la conf).
- Conséquence à documenter : **perdre la clé maître = perdre tous les remotes**, plus seulement les identités SSH.
  Livrer en `-rc` d'abord.

## M11 — Fonctions candidates (à trier selon l'usage)

| Candidat | Intérêt | Coût |
|---|---|---|
| Export / import de la configuration (jobs, remotes, identités chiffrées, réglages) | Migration de machine, reprise après sinistre ; aujourd'hui il faut copier `/data` à froid. | M |
| Jetons d'API personnels (portée par rôle, révocables) | Déclencher un job depuis un script ou une domotique sans session navigateur. | S–M |
| Dupliquer un job | Confort, beaucoup de jobs se ressemblent. | S |
| Canaux de notification supplémentaires (e-mail SMTP, Gotify) | Le webhook générique couvre déjà beaucoup via un relais. | S chacun |
| Graphiques d'historique (durée, volume par job) | Les métriques Prometheus le permettent déjà côté Grafana. | S–M |
| Limite de bande passante par job rclone | **Impossible proprement** : le limiteur rclone est global (spikes.md). À ne pas promettre. | — |

## Ordre proposé

M8 (tout de suite, petit, rattrape la v1.0) → M9.1 et M9.2 → M10 en `-rc` → tri de M11 selon les retours.

## Décisions (2026-11-22)

1. Ordre validé : M8 → M9 → M10 → M11.
2. M11 : tous les candidats sont retenus (sauf la limite de bande passante par job rclone, impossible).
3. Planificateur (M9.3) : on garde `robfig/cron/v3` (voir M9.3).
