# Releases

## Publier une version

1. Vérifier que `main` est vert (workflow `ci`).
2. Créer et pousser un tag annoté au format SemVer :
   ```bash
   git tag -a v1.0.0 -m "v1.0.0"
   git push origin v1.0.0
   ```
   Un suffixe (`v1.1.0-rc.1`) produit une *pre-release* GitHub et ne déplace pas le tag `latest`.
3. Le job `release` du workflow `ci` ne démarre que si `go`, `web`, `e2e` et `docker` réussissent sur ce tag. Il :
   - construit l'image `linux/amd64` + `linux/arm64` et la pousse sur `ghcr.io/<owner>/rcsui` avec les tags
     `1.0.0`, `1.0`, `1` (pas de tag majeur pour `0.x`) et `latest` ;
   - attache un SBOM (SPDX) et une provenance SLSA (`mode=max`) à l'image ;
   - signe l'image par son digest avec **cosign keyless** (identité OIDC du workflow, aucune clé à gérer) ;
   - pousse une attestation de provenance GitHub ;
   - crée la release GitHub (notes générées depuis les PR, commande `docker pull` et digest).

> **Dépôt privé** : la signature et l'attestation sont sautées (avertissement dans le job). L'attestation
> GitHub n'est pas disponible pour les dépôts privés sans plan Enterprise, et une signature keyless inscrit
> l'identité du workflow (donc le nom du dépôt) dans le journal public Rekor. Rendre le dépôt public avant
> la v1.0, ou signer avec une clé gérée.
>
> Le paquet GHCR hérite de la visibilité du dépôt : vérifier qu'il est **public** (Settings du paquet) pour
> que `docker pull` fonctionne sans authentification.

## Vérifier une image

Signature cosign (remplacer `<owner>` et la version) :

```bash
cosign verify ghcr.io/<owner>/rcsui:1.0.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/<owner>/RcsUI/\.github/workflows/ci\.yml@refs/tags/v'
```

Attestation de provenance GitHub :

```bash
gh attestation verify oci://ghcr.io/<owner>/rcsui:1.0.0 --repo <owner>/RcsUI
```

SBOM attaché par buildx :

```bash
docker buildx imagetools inspect ghcr.io/<owner>/rcsui:1.0.0 --format '{{ json .SBOM }}'
```

Pour épingler une version précise, utiliser le digest affiché dans la release
(`ghcr.io/<owner>/rcsui@sha256:…`).
