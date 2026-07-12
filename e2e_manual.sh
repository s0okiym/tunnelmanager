#!/bin/bash
set -u

# Comprehensive manual end-to-end test for tunnel
# Run from repo root.

TUNNEL=./tunnel
PASS=0
FAIL=0
TMPDIR=$(mktemp -d /tmp/tunnel-e2e-XXXXXX)
PIDS=""

cleanup() {
    echo "[cleanup] killing PIDs: $PIDS"
    for p in $PIDS; do kill $p 2>/dev/null || true; done
    sleep 0.2
    for p in $PIDS; do kill -9 $p 2>/dev/null || true; done
    rm -rf "$TMPDIR"
}
trap cleanup EXIT

pass() {
    PASS=$((PASS+1))
    echo "  [PASS] $1"
}
fail() {
    FAIL=$((FAIL+1))
    echo "  [FAIL] $1"
}

free_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

wait_port() {
    local port=$1
    for i in $(seq 1 50); do
        if python3 -c "import socket; s=socket.socket(); s.connect(('127.0.0.1',$port)); s.close()" 2>/dev/null; then
            return 0
        fi
        sleep 0.1
    done
    return 1
}

wait_http() {
    local port=$1
    for i in $(seq 1 50); do
        if curl -fsS http://127.0.0.1:$port/ >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.1
    done
    return 1
}

echo "=== Building tunnel binary ==="
go build -o tunnel main.go || { echo "build failed"; exit 1; }

# ============================================================
echo ""
echo "=== Test 1: Local TCP forwarding (-L) ==="
BACKEND_PORT=$(free_port)
TUNNEL_PORT=$(free_port)
python3 - "$BACKEND_PORT" <<'PY' &
import socket, threading, sys
p = int(sys.argv[1])
def handle(c):
    while True:
        d = c.recv(1024)
        if not d: break
        c.sendall(d)
    c.close()
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', p))
s.listen()
while True:
    c, _ = s.accept()
    threading.Thread(target=handle, args=(c,), daemon=True).start()
PY
PIDS="$PIDS $!"
sleep 0.3
$TUNNEL -L 127.0.0.1:$TUNNEL_PORT:127.0.0.1:$BACKEND_PORT &
PIDS="$PIDS $!"
wait_port $TUNNEL_PORT
MSG="hello-tcp-$RANDOM"
REPLY=$(python3 - "$TUNNEL_PORT" "$MSG" <<'PY'
import socket, sys
p = int(sys.argv[1]); msg = sys.argv[2]
s = socket.socket()
s.connect(('127.0.0.1', p))
s.sendall(msg.encode())
s.shutdown(socket.SHUT_WR)
data = s.recv(1024).decode()
s.close()
print(data)
PY
)
if [ "$REPLY" = "$MSG" ]; then pass "Local TCP echo"; else fail "Local TCP echo got '$REPLY' expected '$MSG'"; fi

# ============================================================
echo ""
echo "=== Test 2: Local UDP forwarding (-L --udp) ==="
BACKEND_PORT=$(free_port)
TUNNEL_PORT=$(free_port)
python3 - "$BACKEND_PORT" <<'PY' &
import socket, sys
p = int(sys.argv[1])
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', p))
while True:
    d, a = s.recvfrom(65535)
    s.sendto(d, a)
PY
PIDS="$PIDS $!"
sleep 0.3
$TUNNEL -L 127.0.0.1:$TUNNEL_PORT:127.0.0.1:$BACKEND_PORT --udp &
PIDS="$PIDS $!"
sleep 0.3
MSG="hello-udp-$RANDOM"
REPLY=$(python3 - "$TUNNEL_PORT" "$MSG" <<'PY'
import socket, sys
p = int(sys.argv[1]); msg = sys.argv[2]
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(2.0)
s.sendto(msg.encode(), ('127.0.0.1', p))
data, _ = s.recvfrom(65535)
s.close()
print(data.decode())
PY
)
if [ "$REPLY" = "$MSG" ]; then pass "Local UDP echo"; else fail "Local UDP echo got '$REPLY' expected '$MSG'"; fi

# ============================================================
echo ""
echo "=== Test 3: Dynamic SOCKS5 proxy (-D) ==="
HTTP_PORT=$(free_port)
SOCKS_PORT=$(free_port)
python3 -m http.server $HTTP_PORT --bind 127.0.0.1 &
PIDS="$PIDS $!"
wait_http $HTTP_PORT
$TUNNEL -D 127.0.0.1:$SOCKS_PORT &
PIDS="$PIDS $!"
sleep 0.3
BODY=$(curl -fsS --socks5 127.0.0.1:$SOCKS_PORT http://127.0.0.1:$HTTP_PORT/)
if [ -n "$BODY" ]; then pass "SOCKS5 proxy fetch"; else fail "SOCKS5 proxy fetch empty body"; fi

# ============================================================
echo ""
echo "=== Test 4: Local TLS forwarding with self-signed cert ==="
HTTP_PORT=$(free_port)
TLS_PORT=$(free_port)
python3 -m http.server $HTTP_PORT --bind 127.0.0.1 &
PIDS="$PIDS $!"
wait_http $HTTP_PORT
$TUNNEL -L 127.0.0.1:$TLS_PORT:127.0.0.1:$HTTP_PORT --tls &
PIDS="$PIDS $!"
sleep 0.5
BODY=$(curl -fsSk https://127.0.0.1:$TLS_PORT/)
if [ -n "$BODY" ]; then pass "TLS self-signed local fetch"; else fail "TLS self-signed local fetch empty"; fi

# ============================================================
echo ""
echo "=== Test 5: Local TLS forwarding with real Let's Encrypt cert + verify ==="
CERT_DIR=/etc/letsencrypt/live/daxiaojie.site
if [ -f "$CERT_DIR/fullchain.pem" ] && [ -f "$CERT_DIR/privkey.pem" ]; then
    HTTP_PORT=$(free_port)
    TLS_PORT=$(free_port)
    python3 -m http.server $HTTP_PORT --bind 127.0.0.1 &
    PIDS="$PIDS $!"
    wait_http $HTTP_PORT
    $TUNNEL -L 127.0.0.1:$TLS_PORT:127.0.0.1:$HTTP_PORT --tls --tls-cert "$CERT_DIR/fullchain.pem" --tls-key "$CERT_DIR/privkey.pem" &
    PIDS="$PIDS $!"
    sleep 0.5
    BODY=$(curl -fsS --resolve "daxiaojie.site:$TLS_PORT:127.0.0.1" --cacert "$CERT_DIR/chain.pem" "https://daxiaojie.site:$TLS_PORT/")
    if [ -n "$BODY" ]; then pass "TLS real cert verify local fetch"; else fail "TLS real cert verify local fetch empty"; fi
else
    fail "TLS real cert verify: cert files missing"
fi

# ============================================================
echo ""
echo "=== Test 6: Remote forwarding with token ==="
CTRL_PORT=$(free_port)
REMOTE_PORT=$(free_port)
BACKEND_PORT=$(free_port)
python3 - "$BACKEND_PORT" <<'PY' &
import socket, threading, sys
p = int(sys.argv[1])
def handle(c):
    while True:
        d = c.recv(1024)
        if not d: break
        c.sendall(d)
    c.close()
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', p))
s.listen()
while True:
    c, _ = s.accept()
    threading.Thread(target=handle, args=(c,), daemon=True).start()
PY
PIDS="$PIDS $!"
sleep 0.3
$TUNNEL -s 127.0.0.1:$CTRL_PORT --token sectok &
PIDS="$PIDS $!"
sleep 0.3
$TUNNEL -R 127.0.0.1:$REMOTE_PORT:127.0.0.1:$BACKEND_PORT -s 127.0.0.1:$CTRL_PORT --token sectok &
PIDS="$PIDS $!"
wait_port $REMOTE_PORT
MSG="hello-remote-$RANDOM"
REPLY=$(python3 - "$REMOTE_PORT" "$MSG" <<'PY'
import socket, sys
p = int(sys.argv[1]); msg = sys.argv[2]
s = socket.socket()
s.connect(('127.0.0.1', p))
s.sendall(msg.encode())
s.shutdown(socket.SHUT_WR)
data = s.recv(1024).decode()
s.close()
print(data)
PY
)
if [ "$REPLY" = "$MSG" ]; then pass "Remote forwarding with token"; else fail "Remote forwarding with token got '$REPLY' expected '$MSG'"; fi

# ============================================================
echo ""
echo "=== Test 7: Remote forwarding with TLS + token + trust-on-first-use ==="
CTRL_PORT=$(free_port)
REMOTE_PORT=$(free_port)
BACKEND_PORT=$(free_port)
IDENTITY_DIR=$TMPDIR/identity-remote
mkdir -p $IDENTITY_DIR
python3 - "$BACKEND_PORT" <<'PY' &
import socket, threading, sys
p = int(sys.argv[1])
def handle(c):
    while True:
        d = c.recv(1024)
        if not d: break
        c.sendall(d)
    c.close()
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', p))
s.listen()
while True:
    c, _ = s.accept()
    threading.Thread(target=handle, args=(c,), daemon=True).start()
PY
PIDS="$PIDS $!"
sleep 0.3
$TUNNEL -s 127.0.0.1:$CTRL_PORT --tls --token sectok &
PIDS="$PIDS $!"
sleep 0.5
$TUNNEL -R 127.0.0.1:$REMOTE_PORT:127.0.0.1:$BACKEND_PORT -s 127.0.0.1:$CTRL_PORT --tls --token sectok --trust-on-first-use &
PIDS="$PIDS $!"
wait_port $REMOTE_PORT
MSG="hello-remote-tls-$RANDOM"
REPLY=$(python3 - "$REMOTE_PORT" "$MSG" <<'PY'
import socket, sys
p = int(sys.argv[1]); msg = sys.argv[2]
s = socket.socket()
s.connect(('127.0.0.1', p))
s.sendall(msg.encode())
s.shutdown(socket.SHUT_WR)
data = s.recv(1024).decode()
s.close()
print(data)
PY
)
if [ "$REPLY" = "$MSG" ]; then pass "Remote TLS+token+TOFU"; else fail "Remote TLS+token+TOFU got '$REPLY' expected '$MSG'"; fi

# ============================================================
echo ""
echo "=== Test 8: Daemon mode control commands ==="
CTRL_SOCK=$TMPDIR/control.sock
LOG_FILE=$TMPDIR/daemon.log
PID_FILE=$TMPDIR/tunneld.pid
DATA_DIR=$TMPDIR/data
mkdir -p $DATA_DIR
CONFIG=$TMPDIR/config.yaml
cat > "$CONFIG" <<EOF
global:
  control_socket: $CTRL_SOCK
  log_file: $LOG_FILE
  data_dir: $DATA_DIR
tunnels:
  - name: web
    mode: local
    local: 127.0.0.1:0
    remote: 127.0.0.1:0
    autostart: true
    group: g1
  - name: socks
    mode: dynamic
    local: 127.0.0.1:0
    autostart: false
    group: g1
EOF

export TUNNEL_CONFIG=$CONFIG
$TUNNEL stop 2>/dev/null || true
$TUNNEL start --background
sleep 1

OUT=$($TUNNEL ls 2>&1)
if echo "$OUT" | grep -q "web"; then pass "daemon ls shows web"; else fail "daemon ls missing web: $OUT"; fi

OUT=$($TUNNEL status 2>&1)
if echo "$OUT" | grep -q "tunnels configured"; then pass "daemon status summary"; else fail "daemon status summary: $OUT"; fi

OUT=$($TUNNEL status web 2>&1)
if echo "$OUT" | grep -q "Status:"; then pass "daemon status web"; else fail "daemon status web: $OUT"; fi

OUT=$($TUNNEL logs --lines 5 2>&1)
if echo "$OUT" | grep -q "manager: starting tunnel web"; then pass "daemon logs"; else fail "daemon logs: $OUT"; fi

OUT=$($TUNNEL start-group g1 2>&1)
OUT=$($TUNNEL ls 2>&1)
if echo "$OUT" | grep -q "socks"; then pass "daemon start-group g1"; else fail "daemon start-group g1: $OUT"; fi

OUT=$($TUNNEL stop-group g1 2>&1)
OUT=$($TUNNEL ls 2>&1)
if echo "$OUT" | grep -q "stopped"; then pass "daemon stop-group g1"; else fail "daemon stop-group g1: $OUT"; fi

OUT=$($TUNNEL disable web 2>&1)
OUT=$($TUNNEL ls 2>&1)
if echo "$OUT" | grep -q "web"; then pass "daemon disable"; else fail "daemon disable: $OUT"; fi

OUT=$($TUNNEL enable web 2>&1)
OUT=$($TUNNEL ls 2>&1)
if echo "$OUT" | grep -q "web"; then pass "daemon enable"; else fail "daemon enable: $OUT"; fi

OUT=$($TUNNEL restart web 2>&1)
OUT=$($TUNNEL ls 2>&1)
if echo "$OUT" | grep -q "web"; then pass "daemon restart"; else fail "daemon restart: $OUT"; fi

OUT=$($TUNNEL save 2>&1)
if [ -f "$CONFIG" ]; then pass "daemon save"; else fail "daemon save config missing"; fi

$TUNNEL add -L 127.0.0.1:0:127.0.0.1:0 --name added --group g2 --autostart=false 2>&1
OUT=$($TUNNEL ls 2>&1)
if echo "$OUT" | grep -q "added"; then pass "daemon add"; else fail "daemon add: $OUT"; fi

OUT=$($TUNNEL rm added 2>&1)
OUT=$($TUNNEL ls 2>&1)
if ! echo "$OUT" | grep -q "added"; then pass "daemon rm"; else fail "daemon rm: $OUT"; fi

# reload: modify config and send SIGHUP
NEW_PORT=$(free_port)
cat > "$CONFIG" <<EOF
global:
  control_socket: $CTRL_SOCK
  log_file: $LOG_FILE
  data_dir: $DATA_DIR
tunnels:
  - name: web
    mode: local
    local: 127.0.0.1:$NEW_PORT
    remote: 127.0.0.1:$NEW_PORT
    autostart: true
    group: g1
EOF
$TUNNEL reload
sleep 0.5
OUT=$($TUNNEL ls 2>&1)
if echo "$OUT" | grep -q "web"; then pass "daemon reload"; else fail "daemon reload: $OUT"; fi

OUT=$($TUNNEL stop 2>&1)
if echo "$OUT" | grep -q "stopped"; then pass "daemon stop"; else fail "daemon stop: $OUT"; fi

# ============================================================
echo ""
echo "=== Summary ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"
if [ $FAIL -eq 0 ]; then
    echo "All manual e2e tests passed."
    exit 0
else
    echo "Some tests failed."
    exit 1
fi
