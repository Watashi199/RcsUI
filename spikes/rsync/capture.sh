#!/bin/sh
# Spike M0 (b) : capture de sorties rsync réelles pour les fixtures du parser.
# Usage (depuis la racine du repo) :
#   wsl sh spikes/rsync/capture.sh testdata/rsync
#   docker run --rm -v "$PWD:/w" -w /w alpine:3.22 sh -c 'apk add rsync && sh spikes/rsync/capture.sh testdata/rsync'
set -eu
OUTROOT=${1:-testdata/rsync}
V=$(rsync --version | head -1 | awk '{print $3}')
O=$OUTROOT/$V
mkdir -p "$O"
echo "rsync $V -> $O"

W=$(mktemp -d)
trap 'chmod -R u+rwX "$W"; rm -rf "$W"' EXIT
SRC=$W/src DST=$W/dst
mkdir -p "$SRC/sub dir" "$SRC/empty" "$DST"
i=0
while [ $i -lt 40 ]; do
  head -c $((i * 37000)) /dev/urandom > "$SRC/sub dir/file $i.bin"
  i=$((i + 1))
done
: > "$SRC/zero.txt"
printf 'x' > "$SRC/$(printf 'new\nline')"
printf 'u' > "$SRC/unicodé ☃.txt"
ln -s "zero.txt" "$SRC/link"
head -c 30000000 /dev/urandom > "$SRC/big.bin"
mkdir -p "$DST/stale"; echo old > "$DST/stale/old.txt"; echo old > "$DST/gone.txt"

run() {
  name=$1; shift
  set +e
  rsync -rlptD --info=progress2,stats2 --no-human-readable --outbuf=L "--out-format=::I %i %l %n" "$@" \
    >"$O/$name.stdout" 2>"$O/$name.stderr"
  echo $? > "$O/$name.exit"
  set -e
}

run dryrun   --delete-after -n -- "$SRC/" "$DST/"
run fullsync --delete-after --bwlimit=20000 -- "$SRC/" "$DST/"
run noop     --delete-after -- "$SRC/" "$DST/"
touch "$SRC/zero.txt"; chmod 600 "$SRC/sub dir/file 3.bin"
run attrs    -- "$SRC/" "$DST/"
run accurate --no-inc-recursive -- "$SRC/" "$W/dst2/"
# code 23 : fichier illisible (ne marche qu'en non-root) + source inexistante
chmod 000 "$SRC/sub dir/file 5.bin"
run code23   -- "$SRC/" "$W/dst3/"
chmod 644 "$SRC/sub dir/file 5.bin"
run code23b  -- "$SRC/does-not-exist" "$W/dst4/"
run code1    --definitely-not-an-option -- "$SRC/" "$W/dst5/"
# annulation : SIGTERM au groupe en cours de transfert lent
(
  set +e
  rsync -rlptD --info=progress2,stats2 --no-human-readable --outbuf=L "--out-format=::I %i %l %n" \
    --bwlimit=2000 -- "$SRC/" "$W/dst6/" >"$O/cancel.stdout" 2>"$O/cancel.stderr"
  echo $? > "$O/cancel.exit"
) &
sleep 3
pkill -TERM -f "dst6" || true
wait
for f in "$O"/*.exit; do printf '%s=%s ' "$(basename "$f" .exit)" "$(cat "$f")"; done; echo
