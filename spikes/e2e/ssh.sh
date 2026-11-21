#!/bin/sh
# Test de bout en bout rsync over SSH (sshd local sur 127.0.0.1:2222).
# Usage : sh spikes/e2e/ssh.sh .tmp/rcsui-linux
set -u
BIN=$(readlink -f "${1:-.tmp/rcsui-linux}")
W=/tmp/rcsui-ssh
rm -rf "$W"
mkdir -p "$W/root/src/sub" "$W/data" "$W/remote"
i=0
while [ $i -lt 10 ]; do head -c 200000 /dev/urandom > "$W/root/src/sub/f$i.bin"; i=$((i + 1)); done

B=http://127.0.0.1:18082
J=$W/jar
PASS=e2e-admin-password
RCSUI_DATA_DIR=$W/data RCSUI_ALLOWED_ROOTS=$W/root RCSUI_LISTEN=127.0.0.1:18082 RCSUI_ADMIN_PASSWORD=$PASS "$BIN" >"$W/server.log" 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; sed -i "/rcsui-e2e/d" "$HOME/.ssh/authorized_keys" 2>/dev/null' EXIT
for _ in $(seq 1 40); do curl -fs $B/api/v1/healthz >/dev/null 2>&1 && break; sleep 0.25; done
field() { sed -E "s/.*\"$1\":\"?([^\",}]*)\"?.*/\1/"; }
CSRF=$(curl -s -c $J -H 'Content-Type: application/json' -d "{\"username\":\"admin\",\"password\":\"$PASS\"}" $B/api/v1/auth/login | field csrfToken)
api() {
	if [ $# -ge 3 ]; then curl -s -b $J -X "$1" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' -d "$3" "$B/api/v1$2"
	else curl -s -b $J -X "$1" -H "X-CSRF-Token: $CSRF" "$B/api/v1$2"; fi
}
wait_state() {
	for _ in $(seq 1 80); do
		s=$(api GET /runs/$1 | field state)
		case "$s" in queued|starting|running|cancelling) sleep 0.25 ;; *) echo "  run -> $s"; return ;; esac
	done
}

echo "sshAvailable: $(api GET /system/info | grep -o '"sshAvailable":[a-z]*')"
echo "== identity"
ID=$(api POST /ssh/identities '{"name":"e2e","generate":true}')
IDN=$(echo "$ID" | grep -o '"id":[0-9]*' | cut -d: -f2)
PUB=$(echo "$ID" | field publicKey)
mkdir -p "$HOME/.ssh" && chmod 700 "$HOME/.ssh"
echo "$PUB rcsui-e2e" >> "$HOME/.ssh/authorized_keys"; chmod 600 "$HOME/.ssh/authorized_keys"
echo "  identity $IDN: $(echo "$ID" | field fingerprint)"

DEST='{"type":"ssh","path":"'$W'/remote/","ssh":{"host":"127.0.0.1","port":2222,"user":"'$(id -un)'","identity":{"managed":'$IDN'}}}'
JOB='{"name":"ssh","engine":"rsync","mode":"sync","source":{"type":"local","path":"'$W'/root/src","contentsOnly":true},"destination":'$DEST',"options":{"v":1,"compress":true}}'
echo "== job before host approval (must be refused)"
api POST /jobs "$JOB" | cut -c1-200; echo

echo "== scan + approve"
SCAN=$(api POST /ssh/known-hosts/scan '{"host":"127.0.0.1","port":2222}')
echo "  scanned: $(echo "$SCAN" | grep -o '"type":"[^"]*"' | tr '\n' ' ')"
echo "  server fingerprint: $(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{print $2}')"
KEY=$(echo "$SCAN" | grep -o '"key":"ssh-ed25519 [^"]*"' | head -1 | cut -d'"' -f4)
api POST /ssh/known-hosts "{\"host\":\"127.0.0.1\",\"port\":2222,\"key\":\"$KEY\"}" | grep -o '"fingerprint":"[^"]*"'
echo "== test connection"
api POST /ssh/test '{"host":"127.0.0.1","port":2222,"user":"'$(id -un)'","identity":{"managed":'$IDN'}}'; echo

echo "== create job"
api POST /jobs "$JOB" | grep -o '"id":[0-9]*' | head -1
echo "== dry-run"
R=$(api POST /jobs/1/run '{"dryRun":true}' | field runId); wait_state "$R"
echo "  items: $(api GET "/runs/$R/items?limit=100" | grep -o '"op":"transfer"' | wc -l) ; remote files: $(ls $W/remote | wc -l)"
echo "== real run"
R=$(api POST /jobs/1/run '{}' | field runId); wait_state "$R"
echo "  remote files: $(ls $W/remote/sub | wc -l) ; filesDone=$(api GET /runs/$R | field filesDone)"
api GET "/runs/$R/logs?limit=3" | grep -o '"msg":"exec [^"]*' | cut -c1-260
echo "  decrypted keys left in run dir: $(ls $W/data/run/ssh 2>/dev/null | wc -l)"
echo "== host key removed (MITM simulation) -> run must fail"
HID=$(api GET /ssh/known-hosts | grep -o '"id":[0-9]*' | head -1 | cut -d: -f2)
api DELETE /ssh/known-hosts/$HID
R=$(api POST /jobs/1/run '{}' | field runId); wait_state "$R"
api GET /runs/$R | field error | cut -c1-200
