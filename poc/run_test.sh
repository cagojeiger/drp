#!/usr/bin/env bash
# run_test.sh — drp POC integration tests
#
# Phase 1: Linear mesh (A—B—C)     → H1, H2, H3, F1, F2
# Phase 2: Triangle mesh (A—B—C—A) → F5 (broadcast loop prevention)
#
# Run:  bash poc/run_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

LOG_DIR="$SCRIPT_DIR/.logs"
rm -rf "$LOG_DIR"
mkdir -p "$LOG_DIR"

PIDS=()
PASS=0
FAIL=0
LOCAL_PORT=15000

BACKEND_DIR=$(mktemp -d)
echo "drp-poc-ok" >"$BACKEND_DIR/index.html"

cleanup() {
	for pid in "${PIDS[@]}"; do
		kill "$pid" 2>/dev/null || true
	done
	wait 2>/dev/null || true
	rm -rf "$BACKEND_DIR" 2>/dev/null || true
}
trap cleanup EXIT

kill_all() {
	for pid in "${PIDS[@]}"; do
		kill "$pid" 2>/dev/null || true
	done
	wait 2>/dev/null || true
	PIDS=()
	sleep 0.5
}

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
NC='\033[0m'

pass() {
	echo -e "  ${GREEN}PASS${NC} $1"
	PASS=$((PASS + 1))
}
fail() {
	echo -e "  ${RED}FAIL${NC} $1"
	FAIL=$((FAIL + 1))
}

wait_port() {
	local port=$1 max=50
	for ((i = 0; i < max; i++)); do
		if python3 -c "import socket; s=socket.socket(); s.settimeout(0.3); s.connect(('localhost', $port)); s.close()" 2>/dev/null; then
			return 0
		fi
		sleep 0.1
	done
	echo -e "  ${RED}TIMEOUT${NC} waiting for port $port"
	return 1
}

http_status() {
	curl -s -o /dev/null -w "%{http_code}" "$@" || echo "000"
}

echo -e "${BOLD}=== drp POC Test ===${NC}"

# =========================================================
# Phase 1: Linear Mesh — H1, H2, H3, F1, F2
# Topology:  A — B — C  (no cycle)
#            drpc → A
# =========================================================
echo
echo -e "${BOLD}--- Phase 1: Linear Mesh (A—B—C) ---${NC}"
echo

# Backend
echo -e "${YELLOW}  backend :${LOCAL_PORT}${NC}"
python3 -m http.server "$LOCAL_PORT" --directory "$BACKEND_DIR" \
	>"$LOG_DIR/local.log" 2>&1 &
PIDS+=($!)
wait_port "$LOCAL_PORT"

# Servers
echo -e "${YELLOW}  drps-A :8001/:9001${NC}"
python3 drps.py --node-id A --http-port 8001 --control-port 9001 -v \
	>"$LOG_DIR/drps-A.log" 2>&1 &
PIDS+=($!)
wait_port 9001

echo -e "${YELLOW}  drps-B :8002/:9002 (peer→A)${NC}"
python3 drps.py --node-id B --http-port 8002 --control-port 9002 \
	--peers localhost:9001 -v >"$LOG_DIR/drps-B.log" 2>&1 &
PIDS+=($!)
wait_port 9002

echo -e "${YELLOW}  drps-C :8003/:9003 (peer→B)${NC}"
python3 drps.py --node-id C --http-port 8003 --control-port 9003 \
	--peers localhost:9002 -v >"$LOG_DIR/drps-C.log" 2>&1 &
PIDS+=($!)
wait_port 9003

sleep 1

echo -e "${YELLOW}  drpc → A (myapp.example.com → :${LOCAL_PORT})${NC}"
python3 drpc.py --server localhost:9001 --alias myapp \
	--hostname myapp.example.com --local localhost:$LOCAL_PORT -v \
	>"$LOG_DIR/drpc.log" 2>&1 &
DRPC_PID=$!
PIDS+=($DRPC_PID)
sleep 1

echo

# H1: Local hit
STATUS=$(http_status -H "Host: myapp.example.com" http://localhost:8001 --max-time 5)
if [ "$STATUS" = "200" ]; then
	pass "H1  local hit (User→A→drpc→local) → $STATUS"
else
	fail "H1  local hit → expected 200, got $STATUS"
fi

# H2: Remote 1-hop relay
STATUS=$(http_status -H "Host: myapp.example.com" http://localhost:8002 --max-time 10)
if [ "$STATUS" = "200" ]; then
	pass "H2  1-hop relay (User→B→A→drpc→local) → $STATUS"
else
	fail "H2  1-hop relay → expected 200, got $STATUS"
fi

# H3: Partial mesh 2-hop relay
STATUS=$(http_status -H "Host: myapp.example.com" http://localhost:8003 --max-time 10)
if [ "$STATUS" = "200" ]; then
	pass "H3  2-hop relay (User→C→B→A→drpc→local) → $STATUS"
else
	fail "H3  2-hop relay → expected 200, got $STATUS"
fi

# F1: Unknown host → broadcast timeout → 502
STATUS=$(http_status -H "Host: unknown.example.com" http://localhost:8002 --max-time 10)
if [ "$STATUS" = "502" ]; then
	pass "F1  unknown host → broadcast timeout → $STATUS"
else
	fail "F1  unknown host → expected 502, got $STATUS"
fi

# F2a: Kill drpc → local_map cleanup → 502
echo -e "\n  ${YELLOW}killing drpc...${NC}"
kill "$DRPC_PID" 2>/dev/null || true
wait "$DRPC_PID" 2>/dev/null || true
sleep 1

STATUS=$(http_status -H "Host: myapp.example.com" http://localhost:8001 --max-time 10)
if [ "$STATUS" = "502" ]; then
	pass "F2a drpc killed → local_map cleaned → $STATUS"
else
	fail "F2a drpc killed → expected 502, got $STATUS"
fi

# F2b: Restart drpc → re-register → 200
echo -e "  ${YELLOW}restarting drpc...${NC}"
python3 drpc.py --server localhost:9001 --alias myapp \
	--hostname myapp.example.com --local localhost:$LOCAL_PORT -v \
	>>"$LOG_DIR/drpc.log" 2>&1 &
DRPC_PID=$!
PIDS+=($DRPC_PID)
sleep 1

STATUS=$(http_status -H "Host: myapp.example.com" http://localhost:8001 --max-time 5)
if [ "$STATUS" = "200" ]; then
	pass "F2b drpc restarted → re-registered → $STATUS"
else
	fail "F2b drpc restarted → expected 200, got $STATUS"
fi

# H5: Concurrent requests via singleflight
STATUS_DIR=$(mktemp -d)
for i in 1 2 3; do
	(curl -s -o /dev/null -w "%{http_code}" -H "Host: myapp.example.com" \
		http://localhost:8002 --max-time 10 >"$STATUS_DIR/$i") &
done
wait
H5_OK=true
for i in 1 2 3; do
	S=$(cat "$STATUS_DIR/$i")
	if [ "$S" != "200" ]; then
		H5_OK=false
		break
	fi
done
rm -rf "$STATUS_DIR"
if [ "$H5_OK" = "true" ]; then
	pass "H5  concurrent requests (3x User→B, singleflight) → all 200"
else
	fail "H5  concurrent requests → expected all 200"
fi

# =========================================================
# Phase 2: Triangle Mesh — F5 (broadcast loop prevention)
# Topology:  A — B
#            |   |      (C connects to both A and B)
#            +- C -+
# =========================================================
echo
echo -e "${BOLD}--- Phase 2: Triangle Mesh (loop test) ---${NC}"
echo

kill_all
sleep 1

# Backend
echo -e "${YELLOW}  backend :${LOCAL_PORT}${NC}"
python3 -m http.server "$LOCAL_PORT" --directory "$BACKEND_DIR" \
	>"$LOG_DIR/local-tri.log" 2>&1 &
PIDS+=($!)
wait_port "$LOCAL_PORT"

# Triangle: B→A, C→A, C→B  (cycle: A—B—C—A)
echo -e "${YELLOW}  drps-A :8001/:9001${NC}"
python3 drps.py --node-id A --http-port 8001 --control-port 9001 -v \
	>"$LOG_DIR/drps-A-tri.log" 2>&1 &
PIDS+=($!)
wait_port 9001

echo -e "${YELLOW}  drps-B :8002/:9002 (peer→A)${NC}"
python3 drps.py --node-id B --http-port 8002 --control-port 9002 \
	--peers localhost:9001 -v >"$LOG_DIR/drps-B-tri.log" 2>&1 &
PIDS+=($!)
wait_port 9002

echo -e "${YELLOW}  drps-C :8003/:9003 (peer→A,B) ← forms triangle${NC}"
python3 drps.py --node-id C --http-port 8003 --control-port 9003 \
	--peers localhost:9001,localhost:9002 -v >"$LOG_DIR/drps-C-tri.log" 2>&1 &
PIDS+=($!)
wait_port 9003

sleep 1
echo

# F5: Broadcast in triangle mesh
# Without seen_messages: WhoHas loops A→B→C→A→... forever
# With seen_messages: terminates quickly, returns 502 in ~3s
START_T=$(date +%s)
STATUS=$(http_status -H "Host: nope.example.com" http://localhost:8002 --max-time 10)
END_T=$(date +%s)
ELAPSED=$((END_T - START_T))

if [ "$STATUS" = "502" ] && [ "$ELAPSED" -lt 8 ]; then
	pass "F5  triangle broadcast loop → $STATUS in ${ELAPSED}s (seen_messages works)"
else
	fail "F5  triangle broadcast loop → expected 502 in <8s, got $STATUS in ${ELAPSED}s"
fi

# =========================================================
# Summary
# =========================================================
echo
echo -e "${BOLD}=== Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC} ==="
echo
echo -e "  H1  local hit              H2  1-hop relay"
echo -e "  H3  2-hop relay            F1  unknown host → 502"
echo -e "  F2a drpc kill → cleanup    F2b drpc restart → recovery"
echo -e "  F5  broadcast loop prevention (triangle mesh)"

if [ "$FAIL" -gt 0 ]; then
	echo -e "\n${YELLOW}Logs: $LOG_DIR/${NC}"
fi

exit "$FAIL"
