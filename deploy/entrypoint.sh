#!/bin/sh
# Point d'entrée : ajuste l'utilisateur rcsui à PUID/PGID puis abandonne les
# privilèges root. Si le conteneur est déjà lancé en non-root (--user), exécute
# directement la commande.
set -eu
# Fichiers créés par l'application (base, logs, config rclone) lisibles par leur seul propriétaire.
umask 077

if [ "$(id -u)" != "0" ]; then
	exec "$@"
fi

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"
case "$PUID$PGID" in
	*[!0-9]*) echo "PUID/PGID must be numeric" >&2; exit 1 ;;
esac
if [ "$PUID" = "0" ] || [ "$PGID" = "0" ]; then
	echo "Refusing to run as root (PUID/PGID=0): the container is designed to run unprivileged." >&2
	exit 1
fi

if [ "$(id -g rcsui)" != "$PGID" ]; then groupmod -o -g "$PGID" rcsui; fi
if [ "$(id -u rcsui)" != "$PUID" ]; then usermod -o -u "$PUID" rcsui; fi

DATA="${RCSUI_DATA_DIR:-/data}"
mkdir -p "$DATA"
# Uniquement la racine et les sous-dossiers de l'application, jamais les données utilisateur.
chown "$PUID:$PGID" "$DATA"
for d in rcsui.db rcsui.db-wal rcsui.db-shm logs backups run ssh home rclone; do
	if [ -e "$DATA/$d" ]; then chown -R "$PUID:$PGID" "$DATA/$d"; fi
done
chmod 0700 "$DATA"

exec su-exec "$PUID:$PGID" "$@"
