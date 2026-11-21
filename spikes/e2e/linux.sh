#!/bin/sh
# Test de bout en bout sous Linux (WSL/CI) : moteur rsync réel, annulation,
# reprise après kill -9 du serveur. Usage : sh spikes/e2e/linux.sh .tmp/rcsui-linux
set -u
BIN=$(readlink -f "${1:-.tmp/rcsui-linux}")
W=/tmp/rcsui-e2e
rm -rf "$W"
mkdir -p "$W/root/src/sub" "$W/data"
i=0
while [ $i -lt 20 ]; do head -c 300000 /dev/urandom > "$W/root/src/sub/f$i.bin"; i=$((i + 1)); done
head -c 40000000 /dev/urandom > "$W/root/src/big.bin"

B=http://127.0.0.1:18081
J=$W/jar
PASS=e2e-admin-password
start() {
	RCSUI_DATA_DIR=$W/data RCSUI_ALLOWED_ROOTS=$W/root RCSUI_LISTEN=127.0.0.1:18081 \
		RCSUI_ADMIN_PASSWORD=$PASS "$BIN" >>"$W/server.log" 2>&1 &
	SRV=$!
	for _ in $(seq 1 40); do curl -fs $B/api/v1/healthz >/dev/null 2>&1 && return; sleep 0.25; done
	echo "server did not start"; cat "$W/server.log"; exit 1
}
api() { # méthode chemin [json]
	if [ $# -ge 3 ]; then
		curl -s -b $J -c $J -X "$1" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' -d "$3" "$B/api/v1$2"
	else
		curl -s -b $J -c $J -X "$1" -H "X-CSRF-Token: $CSRF" "$B/api/v1$2"
	fi
}
field() { sed -E "s/.*\"$1\":\"?([^\",}]*)\"?.*/\1/"; }
wait_state() { # run état
	for _ in $(seq 1 80); do
		s=$(api GET /runs/$1 | field state)
		[ "$s" = "$2" ] && { echo "  run $1 -> $s"; return 0; }
		sleep 0.25
	done
	echo "  FAIL run $1 state=$s want=$2"; return 1
}
login() { CSRF=$(curl -s -c $J -H 'Content-Type: application/json' -d "{\"username\":\"admin\",\"password\":\"$PASS\"}" $B/api/v1/auth/login | field csrfToken); }

echo "== start"; start; login
echo "rsync: $(api GET /system/info | field rsyncVersion)"
JOB='{"name":"e2e","engine":"rsync","mode":"sync","source":{"type":"local","path":"'$W'/root/src","contentsOnly":true},"destination":{"type":"local","path":"'$W'/root/dst"},"options":{"v":1,"excludes":["big.bin"]}}'
api POST /jobs "$JOB" >/dev/null
echo "== dry-run"
R=$(api POST /jobs/1/run '{"dryRun":true}' | field runId); wait_state "$R" succeeded
echo "  items: $(api GET "/runs/$R/items?limit=100" | grep -o '"op":"transfer"' | wc -l) transfers ; dst exists: $([ -e $W/root/dst ] && echo yes || echo no)"
echo "== real sync"
R=$(api POST /jobs/1/run '{}' | field runId); wait_state "$R" succeeded
echo "  files: $(ls $W/root/dst/sub | wc -l) ; filesDone=$(api GET /runs/$R | field filesDone)"

echo "== cancel"
JOB2='{"name":"slow","engine":"rsync","mode":"copy","source":{"type":"local","path":"'$W'/root/src","contentsOnly":true},"destination":{"type":"local","path":"'$W'/root/dst2"},"options":{"v":1,"bwLimitKiB":2000}}'
api POST /jobs "$JOB2" >/dev/null
R=$(api POST /jobs/2/run '{}' | field runId); wait_state "$R" running; sleep 1.5
api POST /runs/$R/cancel >/dev/null; wait_state "$R" cancelled
echo "  rsync procs left: $(pgrep -fc 'dst2' || true)"

echo "== kill -9 during run, then restart"
R=$(api POST /jobs/2/run '{}' | field runId); wait_state "$R" running; sleep 1
kill -9 $SRV; sleep 0.5
echo "  rsync procs after server death: $(pgrep -fc 'dst2' || true) (Pdeathsig)"
start; login
wait_state "$R" interrupted
echo "  error: $(api GET /runs/$R | field error)"
echo "  rsync procs after restart: $(pgrep -fc 'dst2' || true)"

kill $SRV; wait $SRV 2>/dev/null
echo "== graceful stop log tail"; tail -3 "$W/server.log"
