#!/usr/bin/env bash
# Lance l'image RcsUI dans un conteneur jetable et exécute les tests Playwright.
# Usage : e2e/run.sh [image] [arguments playwright...]   (image par défaut : rcsui:e2e)
set -euo pipefail

IMAGE=${1:-rcsui:e2e}
shift || true
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT_HOST=${E2E_HOST_ROOT:-$HERE/.root}
NAME=rcsui-e2e
PORT=${E2E_PORT:-18080}
PASS=e2e-admin-password

rm -rf "$ROOT_HOST"
mkdir -p "$ROOT_HOST"
chmod 777 "$ROOT_HOST"

# Sous Linux, le conteneur écrit avec l'uid de l'utilisateur courant pour que
# les tests puissent relire et nettoyer les fichiers du dossier monté.
ids=()
if [ "$(uname -s)" = "Linux" ]; then ids=(-e "PUID=$(id -u)" -e "PGID=$(id -g)"); fi

# Git Bash (Windows) : pas de conversion automatique des chemins « /e2e » passés
# au conteneur ; le dossier monté est donné au format Windows.
MOUNT=$ROOT_HOST
if command -v cygpath >/dev/null 2>&1; then MOUNT=$(cygpath -w "$ROOT_HOST"); fi
export MSYS_NO_PATHCONV=1

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -p "127.0.0.1:$PORT:8080" \
	-e RCSUI_ADMIN_PASSWORD="$PASS" -e RCSUI_ALLOWED_ROOTS=/e2e "${ids[@]}" \
	-v "$MOUNT:/e2e" "$IMAGE" >/dev/null
cleanup() {
	docker logs "$NAME" >"$HERE/server.log" 2>&1 || true
	docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

for _ in $(seq 1 60); do
	if curl -fsS "http://127.0.0.1:$PORT/api/v1/healthz" >/dev/null 2>&1; then break; fi
	sleep 1
done
curl -fsS "http://127.0.0.1:$PORT/api/v1/healthz" >/dev/null

# Chemin hôte au format natif (Windows : C:\…), lu par Node dans les tests.
export E2E_BASE_URL="http://127.0.0.1:$PORT" E2E_ADMIN_PASSWORD="$PASS" E2E_ROOT=/e2e E2E_HOST_ROOT="$MOUNT"
cd "$HERE"
npx playwright test "$@"
